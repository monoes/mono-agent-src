package coderhost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// fakeJudge is the scripted judge model of the pursuit tests: it keeps every direction and calls every criterion
// progressing, unless a test replaces reply. It counts its calls.
type fakeJudge struct {
	mu     sync.Mutex
	calls  int
	onCall func()                                         // runs inside Complete, before the reply
	reply  func(n int, criteria, directions []string) any // nil: keep everything
	// cost is what each call reports to the host (judgecost); noCost makes a call report nothing, as a runtime that does not
	// measure cost; failWith makes a call fail after it was made (and, unless noCost, charged).
	cost     float64
	noCost   bool
	failWith error
	prompts  []string // every prompt the judge was given
}

func (f *fakeJudge) Pin() string { return "fake/judge@1" }

func (f *fakeJudge) Complete(ctx context.Context, req judge.Request) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.prompts = append(f.prompts, req.System+"\n"+req.User)
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall()
	}
	if !f.noCost {
		c := f.cost
		if c == 0 {
			c = 0.01
		}
		judgecost.Report(ctx, c, true)
	}
	if f.failWith != nil {
		return nil, f.failWith
	}
	var doc struct {
		Criteria   []struct{ ID string } `json:"criteria"`
		Directions []struct{ ID string } `json:"directions"`
	}
	if err := json.Unmarshal([]byte(req.User), &doc); err != nil {
		return nil, err
	}
	var cs, ds []string
	for _, c := range doc.Criteria {
		cs = append(cs, c.ID)
	}
	for _, d := range doc.Directions {
		ds = append(ds, d.ID)
	}
	if f.reply != nil {
		return json.Marshal(f.reply(n, cs, ds))
	}
	type crit struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	type dir struct {
		ID      string `json:"id"`
		Verdict string `json:"verdict"`
		Note    string `json:"note"`
	}
	out := struct {
		Criteria   []crit `json:"criteria"`
		Directions []dir  `json:"directions"`
		Next       []any  `json:"next_actions"`
	}{Criteria: []crit{}, Directions: []dir{}, Next: []any{}}
	for _, id := range cs {
		out.Criteria = append(out.Criteria, crit{id, "progressing", "ok"})
	}
	for _, id := range ds {
		out.Directions = append(out.Directions, dir{id, "keep", "ok"})
	}
	return json.Marshal(out)
}

func (f *fakeJudge) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// pworld is one mission in pursuit: a running mission task session (id 77) bound to the idle chat "m", a confirmed
// goal, a started pursuit, a budget of 10 USD with nothing spent, and a host whose pursuit is wired to a scripted judge
// and a fake clock.
type pworld struct {
	t     *testing.T
	h     *Host
	st    *Store
	clk   *epoch.FakeClock
	model *fakeJudge
	// leadPin is the runtime/model the lead of the world runs, as the host reads it at each gate and each judge call.
	leadPin string
}

const pursuitSession = 77

func newPursuitWorld(t *testing.T) *pworld { return newPursuitWorldWith(t, true) }

// newPursuitWorldLimits is the world with a pursuit started with the given away limits.
func newPursuitWorldLimits(t *testing.T, lim missionpursuit.AwayLimits) *pworld {
	startLimits = lim
	defer func() { startLimits = missionpursuit.AwayLimits{} }()
	return newPursuitWorldWith(t, true)
}

var startLimits missionpursuit.AwayLimits

// startCriteria are the goal criteria of the world; nil is the one rubric criterion.
var startCriteria []missionpursuit.Criterion

// newPursuitWorldWith is the world with or without the started pursuit row.
func newPursuitWorldWith(t *testing.T, started bool) *pworld {
	t.Helper()
	h, _, st := newHost(t)
	clk := epoch.NewFakeClock(time.Now().UTC().Truncate(time.Second))
	st.now = clk.Now
	w := &pworld{t: t, h: h, st: st, clk: clk, model: &fakeJudge{}}
	addConversation(t, st, "m", "/work/m")
	if _, err := st.EnsureSession("p", "m"); err != nil {
		t.Fatal(err)
	}
	w.exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'p')`)
	w.exec(`INSERT INTO task_sessions (id, profile_id, conversation_id, title, state, kind, budget_usd, created_by, created_at, updated_at) VALUES (77, 'p', 'm', 't', 'running', 'mission', 10, 'you', ?, ?)`, clk.Now().Format(time.RFC3339), clk.Now().Format(time.RFC3339))
	w.exec(`UPDATE coder_sessions SET chat_mode = 'mission', budget_usd = 0.2 WHERE profile_id = 'p' AND conversation_id = 'm'`) // a lead with its own cap
	if started {
		mp := missionpursuit.NewStore(st.db)
		op := tasks.Actor{Kind: tasks.Human, Name: "you"}
		if _, err := mp.Set(context.Background(), "p", 77, criteriaOfTheWorld(), op); err != nil {
			t.Fatal(err)
		}
		if _, err := mp.Start(context.Background(), "p", 77, startLimits, op); err != nil {
			t.Fatal(err)
		}
		clk.Advance(2 * time.Second) // everything after the start is after the person's last action
	}
	w.exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, kind, created_at, updated_at) VALUES ('p', 'open work', 'ready', 0, 77, 'task', 'x', 'x')`)
	h.Logf = t.Logf
	w.leadPin = "claude/lead-model"
	h.Pursuit = &PursuitDeps{Model: w.model, Refuse: func(string) bool { return false }, Clock: clk,
		WakeCapUSD: DefaultWakeCapUSD, LeadPin: func(string, string) string { return w.leadPin },
		Interval: 30 * time.Minute, EvidenceGap: 30 * time.Minute}
	return w
}

func (w *pworld) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.db.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

// pass runs one pursuit pass now.
func (w *pworld) pass() { w.t.Helper(); w.h.pursuitPass(context.Background()) }

// advance moves the pursuit's clock (and the store's, which is the same one).
func (w *pworld) advance(d time.Duration) { w.clk.Advance(d) }

func (w *pworld) epochs() []string {
	w.t.Helper()
	rows, err := w.st.db.Query(`SELECT epoch_seq || ':' || state FROM mission_strategy_epochs WHERE session_id = 77 ORDER BY epoch_seq`)
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

func (w *pworld) wakes() []Command {
	w.t.Helper()
	cmds, err := w.st.Pending()
	if err != nil {
		w.t.Fatal(err)
	}
	var out []Command
	for _, c := range cmds {
		if c.ConversationID == "m" && c.Kind == KindSend && strings.HasPrefix(c.TurnID, PursuitTurnPrefix) {
			out = append(out, c)
		}
	}
	return out
}

func (w *pworld) waiting() string {
	w.t.Helper()
	s, ok, err := w.st.pursuitStateOf(pursuitSession)
	if err != nil || !ok {
		w.t.Fatalf("pursuit state: %+v ok=%v err=%v", s, ok, err)
	}
	return s.Waiting
}

func (w *pworld) pause() {
	w.exec(`UPDATE mission_pursuit SET paused_by = 'you', updated_at = ? WHERE session_id = 77`, w.clk.Now().Format(time.RFC3339))
}

func (w *pworld) resume() {
	w.exec(`UPDATE mission_pursuit SET paused_by = '', updated_at = ? WHERE session_id = 77`, w.clk.Now().Format(time.RFC3339))
}

// park adds n open questions from the lead's chat, created now.
func (w *pworld) park(n int) {
	for i := 0; i < n; i++ {
		w.exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, question, options, default_answer, reversible, status, answer, answered_by, command_id, created_at, answered_at)
			VALUES ('p', 'm', 'ambiguity', 'which?', '[]', '', 1, 'open', '', '', 0, ?, '')`, w.clk.Now().Format(time.RFC3339Nano))
	}
}

func (w *pworld) setSpent(usd float64) {
	w.exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = 77`, usd)
}

// openDirection adds an open direction with an optional lead session (a direction session under the mission).
func (w *pworld) openDirection(id, title string, leadSession int64) {
	lead := ""
	if leadSession > 0 {
		lead = itoa(leadSession)
		w.exec(`INSERT INTO task_sessions (id, profile_id, title, state, kind, parent_session_id, created_by, created_at, updated_at) VALUES (?, 'p', 'd', 'running', 'direction', 77, 'you', 'x', 'x')`, leadSession)
	}
	w.exec(`INSERT INTO mission_strategy_directions (session_id, direction_id, key_hash, title, state, lead_session, created_epoch) VALUES (77, ?, ?, ?, 'proposed', ?, 0)`,
		id, sha256Of(id), title, lead)
}

// startedWith says whether a turn whose id starts with prefix was started.
func (l *fakeLauncher) startedWith(prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.started {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func sha256Of(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func goalOf(w *pworld) missionpursuit.Goal {
	g, err := missionpursuit.NewStore(w.st.db).Get(context.Background(), "p", pursuitSession)
	if err != nil {
		w.t.Fatal(err)
	}
	return g
}

func criteriaOfTheWorld() []missionpursuit.Criterion {
	if startCriteria != nil {
		return startCriteria
	}
	return []missionpursuit.Criterion{{Kind: missionpursuit.KindRubric, Spec: "it is good"}}
}

// newPursuitWorldCriteria is the world with a goal of the given criteria (c1, c2, ...).
func newPursuitWorldCriteria(t *testing.T, cs ...missionpursuit.Criterion) *pworld {
	startCriteria = cs
	defer func() { startCriteria = nil }()
	return newPursuitWorldWith(t, true)
}
