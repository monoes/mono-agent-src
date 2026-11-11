package coderhost

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// cworld is a pursuit world whose goal is checkable (a file criterion and a rubric one), whose tasks are settled, whose lead is
// idle and not unattended (so the loop spends nothing else), whose project folder is a real temporary folder, and whose goal checks
// run a scripted verifier.
type cworld struct {
	*pworld
	root string
	vmu  sync.Mutex
	fn   func(ctx context.Context, req verifier.Request) (verifier.Result, error)
	reqs []verifier.Request
	dep  *VerifierDeps
	// releases make a blocking verifier end once, whoever asks first
	releases map[chan struct{}]*sync.Once
}

type scriptVerifier struct{ w *cworld }

func (scriptVerifier) Pin() string { return "gemini/checker-1" }
func (s scriptVerifier) Run(ctx context.Context, req verifier.Request) (verifier.Result, error) {
	s.w.vmu.Lock()
	s.w.reqs = append(s.w.reqs, req)
	fn := s.w.fn
	s.w.vmu.Unlock()
	return fn(ctx, req)
}

func (w *cworld) requests() []verifier.Request {
	w.vmu.Lock()
	defer w.vmu.Unlock()
	return append([]verifier.Request(nil), w.reqs...)
}

// resultOf is a verifier result for a request: every machine criterion passes unless outcome says otherwise.
func resultOf(req verifier.Request, o verifier.Outcome, reason verifier.Reason, micro int64, known bool) verifier.Result {
	res := verifier.Result{Outcome: o, Reason: reason, GoalHash: req.GoalHash, SubjectDigest: req.ExpectDigest, SubjectFiles: 1, Containment: verifier.ContainmentFileOnly,
		ReviewerPin: "gemini/checker-1", CostMicro: micro, CostKnown: known}
	for _, c := range req.Criteria {
		cr := verifier.CriterionResult{ID: c.ID, Hash: c.Hash, Kind: c.Kind, Result: verifier.ResPass}
		if c.Kind == "rubric" {
			cr.Result = verifier.ResPerson
		}
		res.Criteria = append(res.Criteria, cr)
	}
	if o == verifier.OutcomePassedMachine {
		res.ReviewerVerdict, res.ReviewerObserved = "ok", "gemini/checker-1"
	}
	return res
}

func newCW(t *testing.T) *cworld {
	t.Helper()
	pw := newPursuitWorldCriteria(t,
		missionpursuit.Criterion{Kind: "file", Spec: "a.txt", Expected: "exists"},
		missionpursuit.Criterion{Kind: missionpursuit.KindRubric, Spec: "it is good"})
	w := &cworld{pworld: pw, root: t.TempDir(), releases: map[chan struct{}]*sync.Once{}}
	if err := os.WriteFile(filepath.Join(w.root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE ai_chat_conversations SET cwd = ? WHERE id = 'm'`, w.root)
	w.exec(`UPDATE coder_sessions SET unattended = 0 WHERE conversation_id = 'm'`)
	w.exec(`UPDATE tasks SET status = 'done' WHERE session_id = 77`)
	w.fn = func(_ context.Context, req verifier.Request) (verifier.Result, error) {
		return resultOf(req, verifier.OutcomeFailed, verifier.ReasonCriteriaFailed, 0, true), nil
	}
	w.dep = &VerifierDeps{Pin: "gemini/checker-1", Scratch: t.TempDir(), Forbidden: []string{"/nonexistent-home"},
		New:      func(verifier.Meter) (verifier.Verifier, error) { return scriptVerifier{w}, nil },
		QuietFor: 2 * time.Minute, PrepEvery: 30 * time.Second, WatchEvery: 5 * time.Millisecond}
	w.h.Pursuit.Verifier = w.dep
	t.Cleanup(func() { w.h.checkWG.Wait() })
	return w
}

// wait waits for every check goroutine started so far.
func (w *cworld) wait() { w.t.Helper(); w.h.checkWG.Wait() }

// step runs one pass, lets the check goroutine it started finish, and moves the clock a preparation period on.
func (w *cworld) step() {
	w.t.Helper()
	w.pass()
	w.wait()
	w.advance(31 * time.Second)
}

// untilChecked steps until the mission has n check rows (or gives up after 12 steps: the quiet wait is 2 minutes of 31 s steps).
func (w *cworld) untilChecked(n int) {
	w.t.Helper()
	for i := 0; i < 12 && w.checkRows() < n; i++ {
		w.step()
	}
}

func (w *cworld) checkRows() int {
	w.t.Helper()
	var n int
	if err := w.st.db.QueryRow(`SELECT COUNT(*) FROM mission_goal_checks WHERE session_id = 77`).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// checks reads the check rows as "round:state:outcome:reason".
func (w *cworld) checks() []string {
	w.t.Helper()
	rows, err := w.st.db.Query(`SELECT round || ':' || state || ':' || outcome || ':' || reason FROM mission_goal_checks WHERE session_id = 77 ORDER BY id`)
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

func (w *cworld) spend() missionpursuit.CheckSpend {
	w.t.Helper()
	s, err := missionpursuit.CheckSpendAfter(context.Background(), w.st.db, 77, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

// touch writes the project, so the next look sees a changed tree.
func (w *cworld) touch(content string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.root, "a.txt"), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
	later := w.clk.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(w.root, "a.txt"), later, later)
}

// personActs is an action of the person the loop measures from: the pursuit row moves, as a limit change or a resume does.
func (w *cworld) personActs() {
	w.t.Helper()
	w.advance(2 * time.Second)
	w.exec(`UPDATE mission_pursuit SET updated_at = ? WHERE session_id = 77`, w.clk.Now().UTC().Format(time.RFC3339))
	w.advance(2 * time.Second)
	w.pass() // the gate sees it and moves the base of the counts
	w.wait()
}

// rawCheck writes a finished check row by hand (a check starts running, then ends), for the limits' tests.
func (w *cworld) rawCheck(round int64, outcome, reason string, micro int64, known int) {
	w.t.Helper()
	at := w.clk.Now().UTC().Format(time.RFC3339)
	w.exec(`INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, subject_root, started_at, written_by) VALUES (77, ?, ?, 'running', ?, ?, 'host-verifier')`, goalOf(w.pworld).Hash, round, w.root, at)
	w.exec(`UPDATE mission_goal_checks SET state = 'done', outcome = ?, reason = ?, cost_micro = ?, cost_known = ?, ended_at = ? WHERE session_id = 77 AND round = ?`, outcome, reason, micro, known, at, round)
}
