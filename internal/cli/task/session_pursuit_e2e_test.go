package task

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
	"github.com/monoes/mono-agent/internal/storage"
)

// The end-to-end world of the pursuit acceptance tests: a mission started through the REAL verbs (goal, preview, begin) on a
// scratch database, then the real coder host loop over the same file with a scripted judge, a fake launcher and a fake
// epoch clock. No model, no process, no network.
//
// The clock seam: the CLI stamps what the person does in real whole seconds, and the host reads those stamps against its
// own; the world does not sleep, it ages what the person did (personActed): every person action is moved to a fixed
// distance in the past, one minute after the one before, so "after" is deterministic. The epoch clock (the engine's timer)
// is the fake one and moves only forward.

// e2eJudge is the scripted judge: it keeps every direction, calls every criterion progressing, counts its calls and can fail.
type e2eJudge struct {
	mu       sync.Mutex
	calls    int
	failWith error
}

func (j *e2eJudge) Pin() string { return "fake/judge@1" }

func (j *e2eJudge) count() int { j.mu.Lock(); defer j.mu.Unlock(); return j.calls }

func (j *e2eJudge) Complete(ctx context.Context, req judge.Request) ([]byte, error) {
	j.mu.Lock()
	j.calls++
	fail := j.failWith
	j.mu.Unlock()
	judgecost.Report(ctx, 0.01, true)
	if fail != nil {
		return nil, fail
	}
	var doc struct {
		Criteria   []struct{ ID string } `json:"criteria"`
		Directions []struct{ ID string } `json:"directions"`
	}
	if err := json.Unmarshal([]byte(req.User), &doc); err != nil {
		return nil, err
	}
	type item struct {
		ID      string `json:"id"`
		Status  string `json:"status,omitempty"`
		Verdict string `json:"verdict,omitempty"`
		Note    string `json:"note"`
	}
	out := struct {
		Criteria   []item `json:"criteria"`
		Directions []item `json:"directions"`
		Next       []any  `json:"next_actions"`
	}{Criteria: []item{}, Directions: []item{}, Next: []any{}}
	for _, c := range doc.Criteria {
		out.Criteria = append(out.Criteria, item{ID: c.ID, Status: "progressing", Note: "ok"})
	}
	for _, d := range doc.Directions {
		out.Directions = append(out.Directions, item{ID: d.ID, Verdict: "keep", Note: "ok"})
	}
	return json.Marshal(out)
}

// e2eLauncher records what the host starts; no process exists. Its turns end when the test does.
type e2eLauncher struct {
	mu      sync.Mutex
	started []string // turn ids
	caps    []float64
	turns   []*e2eTurn
}

type e2eTurn struct {
	done chan struct{}
	once sync.Once
	pid  int
}

func (e *e2eTurn) PID() int      { return e.pid }
func (e *e2eTurn) Stamp() string { return "stamp" }
func (e *e2eTurn) Wait() error   { <-e.done; return nil }
func (e *e2eTurn) Stop()         { e.once.Do(func() { close(e.done) }) }

func (l *e2eLauncher) Start(profile, conv, turn, msg string, att []string, token string) (coderhost.Turn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := &e2eTurn{done: make(chan struct{}), pid: 1000 + len(l.turns)}
	l.turns = append(l.turns, t)
	l.started = append(l.started, turn)
	return t, nil
}

func (l *e2eLauncher) StartCapped(profile, conv, turn, msg string, att []string, token string, capUSD float64) (coderhost.Turn, error) {
	l.mu.Lock()
	l.caps = append(l.caps, capUSD)
	l.mu.Unlock()
	return l.Start(profile, conv, turn, msg, att, token)
}

func (l *e2eLauncher) Adopt(int, string) coderhost.Turn       { return nil }
func (l *e2eLauncher) Finish(profile, conv, turn, why string) {}

func (l *e2eLauncher) startedWith(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.started {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func (l *e2eLauncher) endAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, t := range l.turns {
		t.Stop()
	}
}

// e2eWorld is one mission (a draft made running by approval, with one open task) in pursuit, begun by the CLI.
type e2eWorld struct {
	beginWorld
	t     *testing.T
	d     *storage.Database
	st    *coderhost.Store
	h     *coderhost.Host
	l     *e2eLauncher
	clk   *epoch.FakeClock
	model *e2eJudge
	acts  int // person actions aged so far
	// deps is what the host's pursuit is given; a test changes it before the first pass.
	deps *coderhost.PursuitDeps
}

// newE2EWorld is a mission with its pursuit begun by the real verbs and a host wired over the same database.
func newE2EWorld(t *testing.T, limits ...string) *e2eWorld {
	t.Helper()
	w := wireE2E(t, newBeginWorld(t))
	w.begin(limits...)
	return w
}

// wireE2E opens the host side over a mission whose pursuit is not begun yet.
func wireE2E(t *testing.T, bw beginWorld) *e2eWorld {
	t.Helper()
	d, err := storage.NewDatabase(bw.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	w := &e2eWorld{beginWorld: bw, t: t, d: d, st: coderhost.NewStore(d.DB), l: &e2eLauncher{}, model: &e2eJudge{},
		clk: epoch.NewFakeClock(time.Now().UTC().Truncate(time.Second))}
	t.Cleanup(func() {
		w.l.endAll()
		if w.h != nil {
			w.h.Wait()
		}
	})
	w.sql(`UPDATE coder_sessions SET state = 'idle', turn_id = '', turn_pid = 0, turn_stamp = '' WHERE conversation_id = 'chat1'`)
	w.h = &coderhost.Host{Store: w.st, L: w.l, Logf: t.Logf}
	w.deps = &coderhost.PursuitDeps{Model: w.model, Refuse: func(string) bool { return false }, Clock: w.clk,
		WakeCapUSD: coderhost.DefaultWakeCapUSD, LeadPin: func(string, string) string { return "claude/opus" },
		Interval: 30 * time.Minute, EvidenceGap: 30 * time.Minute}
	w.h.Pursuit = w.deps
	return w
}

// begin reads the consent block and begins with it, as the person does, then approves the plan of one open task.
func (w *e2eWorld) begin(limits ...string) {
	w.t.Helper()
	p := w.preview(w.t, limits...)
	var begun pursuitDoc
	mustTaskJSON(w.t, w.db, "default", &begun, "", w.beginArgs(p, limits...)...)
	if !begun.Pursuit.Enabled || begun.Pursuit.LockedBy != "you" {
		w.t.Fatalf("begin: %+v", begun.Pursuit)
	}
	sessionAdd(w.t, w.db, w.s, "the work")
	mustTaskJSON(w.t, w.db, "default", &sessionDoc{}, "", "session", "approve", id(w.s))
	w.agePersonAction() // the begin is the person's first action; everything after it is after it
	// the approval of the plan (a person action too: the "approved" event and the session's stamp) is aged with it
	aged := agedStamp()
	w.sql(`UPDATE task_session_events SET at = ? WHERE session_id = ? AND kind = 'approved'`, aged, w.s)
	w.sql(`UPDATE task_sessions SET approved_at = ? WHERE id = ?`, aged, w.s)
}

func (w *e2eWorld) sql(q string, args ...any) {
	w.t.Helper()
	if _, err := w.d.DB.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

// agePersonAction moves the pursuit row's stamp (what the CLI wrote in real seconds) to an hour-scale past, one minute after
// the previous action's.
func (w *e2eWorld) agePersonAction() {
	w.t.Helper()
	at := time.Now().UTC().Add(-3 * time.Hour).Add(time.Duration(w.acts) * time.Minute)
	w.acts++
	w.sql(`UPDATE mission_pursuit SET updated_at = ? WHERE session_id = ?`, at.Format(time.RFC3339), w.s)
}

// personActed runs a person verb of the pursuit and ages its stamp.
func (w *e2eWorld) personActed(verb string) pursuitDoc {
	w.t.Helper()
	var doc pursuitDoc
	mustTaskJSON(w.t, w.db, "default", &doc, "", "session", "pursuit", verb, id(w.s))
	w.agePersonAction()
	return doc
}

func (w *e2eWorld) pass()                   { w.t.Helper(); w.h.PursuitPassForTest(w.t) }
func (w *e2eWorld) advance(d time.Duration) { w.clk.Advance(d) }
func (w *e2eWorld) setSpent(usd float64) {
	w.sql(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, usd, w.s)
}
func (w *e2eWorld) tick() {
	w.t.Helper()
	if err := w.h.Tick(); err != nil {
		w.t.Fatal(err)
	}
}
func (w *e2eWorld) finishTurns() { w.l.endAll(); w.h.Wait(); w.leadIdle() }
func (w *e2eWorld) leadIdle() {
	w.sql(`UPDATE coder_sessions SET state = 'idle', turn_id = '', turn_pid = 0, turn_stamp = '' WHERE conversation_id = 'chat1'`)
}
func (w *e2eWorld) wakeCount() int { return w.l.startedWith(coderhost.PursuitTurnPrefix) }

// status is `pursuit status --json`, the person's own view.
type e2eStatus struct {
	Progress missionpursuit.Progress `json:"progress"`
}

func (w *e2eWorld) status() missionpursuit.Progress {
	w.t.Helper()
	var s e2eStatus
	mustTaskJSON(w.t, w.db, "default", &s, "", "session", "pursuit", "status", id(w.s))
	return s.Progress
}

func (w *e2eWorld) epochs() []string {
	w.t.Helper()
	rows, err := w.d.DB.Query(`SELECT epoch_seq || ':' || state FROM mission_strategy_epochs WHERE session_id = ? ORDER BY epoch_seq`, w.s)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// nextEpoch makes the timer due and runs a pass; the first pass of a run only starts the engine.
func (w *e2eWorld) nextEpoch() {
	w.t.Helper()
	w.pass()
	w.advance(31 * time.Minute)
	w.pass()
}

func (w *e2eWorld) park(n int) {
	for i := 0; i < n; i++ {
		w.sql(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, question, options, default_answer, reversible, status, answer, answered_by, command_id, created_at, answered_at)
			VALUES ('default', 'chat1', 'ambiguity', 'which?', '[]', '', 1, 'open', '', '', 0, ?, '')`, time.Now().UTC().Format(time.RFC3339Nano))
	}
}

// agedStamp is a person action's time: an hour-scale past, so that everything the host writes now is after it.
func agedStamp() string { return time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339) }
