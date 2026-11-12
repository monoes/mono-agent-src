package coderhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"github.com/monoes/mono-agent/internal/monomind"
)

// stubReviewer is the checker model: it answers "ok" and reports a cost. It counts its calls.
type stubReviewer struct {
	mu    sync.Mutex
	calls int
}

func (r *stubReviewer) exec(_ context.Context, _ monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	on(monomind.Event{Type: monomind.EventStart, Runtime: "gemini", Model: "checker-1"})
	return &monomind.TurnResult{SawDone: true, ResultText: `{"verdict":"ok","reason":"fine"}`, CostUSD: 0.12, HasCostUSD: true}, nil
}

func (r *stubReviewer) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

// The whole way, scripted: the pursuit judges and wakes the lead while there is work; the lead (faked) finishes it; the money
// stops; the real Checker, over a stub reviewer model and a fake confiner, runs check 1, which fails on a file that is not there;
// the lead (faked) writes it; check 2 passes its machine criteria and the reviewer, the pursuit waits for the person's answers,
// and nothing spends meanwhile. Run it in a private network namespace (`unshare -rn`) with a scratch HOME: nothing here needs a
// network, a real model or a real runtime.
func TestAScriptedPursuitFromWorkToAChecksAnswer(t *testing.T) {
	// File criteria only: a command criterion re-executes the test binary as the sandbox's limits helper, which this package's
	// test main does not serve (the verifier package's own tests run real programs under a fake confiner).
	crit := []missionpursuit.Criterion{{Kind: "file", Spec: "a.txt", Expected: "exists"}, {Kind: "file", Spec: "b.txt", Expected: "exists"}, {Kind: missionpursuit.KindRubric, Spec: "it is good"}}
	pw := newPursuitWorldCriteria(t, crit...)
	w := &cworld{pworld: pw, root: t.TempDir(), releases: map[chan struct{}]*sync.Once{}}
	if err := os.WriteFile(filepath.Join(w.root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE ai_chat_conversations SET cwd = ? WHERE id = 'm'`, w.root)
	rev, conf := &stubReviewer{}, &verifier.FakeConfiner{Label: "fake"}
	scratch := t.TempDir()
	w.h.Pursuit.Verifier = &VerifierDeps{Pin: "gemini/checker-1", Scratch: scratch, Confiner: conf, Forbidden: ForbiddenFolders{Ancestors: []string{"/nonexistent-home"}},
		QuietFor: 2 * time.Minute, PrepEvery: 30 * time.Second, WatchEvery: 20 * time.Millisecond,
		New: func(m verifier.Meter) (verifier.Verifier, error) {
			return verifier.New(verifier.Config{Checker: "gemini/checker-1", ScratchBase: scratch, Meter: m, Exec: rev.exec, Confiner: conf,
				Workspace: func() (string, error) { return t.TempDir(), nil }, TotalTimeout: time.Minute})
		}}
	t.Cleanup(func() { w.h.checkWG.Wait() })

	// 1. there is work: the loop judges and wakes the lead
	w.firstEpoch()
	if w.model.count() != 1 || len(w.wakes()) != 1 {
		t.Fatalf("with work to do: %d judge call(s), %d wake-up(s), want 1 and 1", w.model.count(), len(w.wakes()))
	}
	w.endCommandsDone()
	if w.checkRows() != 0 {
		t.Fatalf("a check started while there is work: %v", w.checks())
	}
	// 2. the lead finishes the work: one close wake, then the money stops
	w.exec(`UPDATE tasks SET status = 'done' WHERE session_id = 77`)
	w.touchTasks()
	w.advance(31 * time.Minute)
	for i := 0; i < 3; i++ {
		w.pass()
		w.wait()
		w.endCommandsDone()
		w.advance(31 * time.Minute)
	}
	judges, wakes := w.model.count(), w.wakeRows()
	// 3. check 1: b.txt is not there
	w.untilChecked(1)
	if got := w.checks(); len(got) != 1 || got[0] != "1:done:failed:criteria-failed" {
		t.Fatalf("check 1 = %v", got)
	}
	if rev.count() != 0 || w.spend().Micro != 0 {
		t.Errorf("a check that failed on its machine criteria asked the checker (%d) or cost %d", rev.count(), w.spend().Micro)
	}
	// the failed check is a finish park: it earns ONE epoch (trigger finish-parked) and ONE wake-up of the lead, in fixed words that name
	// the criterion that does not pass and nothing the lead or the checker wrote; then nothing more spends, and the same project is
	// not checked again
	for i := 0; i < 8; i++ {
		w.step()
	}
	if w.checkRows() != 1 {
		t.Fatalf("the same project was checked again: %v", w.checks())
	}
	if tr := w.epochTriggers(); tr[len(tr)-1] != "finish-parked" {
		t.Fatalf("the failed check did not earn a finish-parked epoch: %v", tr)
	}
	parkWakes := 0
	for _, text := range w.allWakeTexts() {
		if strings.Contains(text, "Goal check 1 (round 1) failed") {
			parkWakes++
			if !strings.Contains(text, "not passing: c2") {
				t.Errorf("the wake-up for the failed check does not name c2: %s", text)
			}
			noBanned(t, "scripted failed check", text)
		}
	}
	if parkWakes != 1 {
		t.Fatalf("wake-ups about the failed check: %d, want 1 (%v)", parkWakes, w.allWakeTexts())
	}
	w.endCommandsDone()
	// 4. the lead writes the missing file: check 2 passes and waits for the person
	if err := os.WriteFile(filepath.Join(w.root, "b.txt"), []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.touchTasks() // the lead's work changes a task: one more close wake, which the lead takes before the check
	for i := 0; i < 14 && w.checkRows() < 2; i++ {
		w.step()
		w.endCommandsDone()
	}
	judges, wakes = w.model.count(), w.wakeRows()
	if got := w.checks(); len(got) != 2 || got[1] != "2:done:passed-machine:" {
		t.Fatalf("checks = %v", got)
	}
	if rev.count() != 1 {
		t.Errorf("the checker was asked %d times, want once", rev.count())
	}
	if s := w.spend(); s.Micro != 120000 || s.Count != 2 {
		t.Errorf("spend %+v, want $0.12 over 2 checks", s)
	}
	if conf.Tests != 0 || conf.Placed != 0 {
		t.Errorf("file criteria ran a self test (%d) or placed a program (%d)", conf.Tests, conf.Placed)
	}
	// 5. the answer is the person's: nothing spends meanwhile, and nothing accepts
	for i := 0; i < 6; i++ {
		w.pass()
		w.wait()
		w.endCommandsDone()
		w.advance(31 * time.Minute)
	}
	if got := w.waiting(); got != pursuitWaitingAnswer {
		t.Errorf("waiting = %q, want %q", got, pursuitWaitingAnswer)
	}
	if w.model.count() != judges || w.wakeRows() != wakes || rev.count() != 1 || w.checkRows() != 2 {
		t.Errorf("spent while the person's answer is owed: judge %d->%d, wakes %d->%d, reviewer %d, checks %d", judges, w.model.count(), wakes, w.wakeRows(), rev.count(), w.checkRows())
	}
	var accepted int
	if err := w.st.db.QueryRow(`SELECT COUNT(*) FROM mission_goal_checks WHERE outcome IN ('accepted','rejected')`).Scan(&accepted); err != nil || accepted != 0 {
		t.Errorf("the host accepted or rejected a check: %d %v", accepted, err)
	}
	var events int
	if err := w.st.db.QueryRow(`SELECT COUNT(*) FROM task_session_events WHERE actor = 'host-verifier'`).Scan(&events); err != nil || events != 0 {
		t.Errorf("the host verifier wrote %d task event(s)", events)
	}
	if cs, err := missionpursuit.ReadCheckStatus(context.Background(), w.st.db, 77, w.clk.Now()); err != nil || !cs.Facts.AwaitingPerson || cs.Window.Count != 2 {
		t.Errorf("status %+v %v", cs, err)
	}
	// 6. the person answers c3 (the rubric criterion) with a yes, over the digest of the project as it is now: the check is accepted,
	// by them. The host then wakes the lead ONCE to call mission_finish; no check starts, no epoch opens, the money stays still.
	ps := missionpursuit.NewStore(w.st.db)
	c, err := ps.LatestCheck(context.Background(), "p", 77, true)
	if err != nil {
		t.Fatal(err)
	}
	now, err := digestTree(context.Background(), w.root, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AnswerRubric(context.Background(), "p", 77, missionpursuit.RubricAnswer{Check: c.ID, Criterion: "c3", Yes: true, Note: "ok", Dossier: c.Dossier, SubjectNow: now.Digest}, person); err != nil {
		t.Fatal(err)
	}
	epochs, wakesBefore := len(w.epochTriggers()), w.wakeRows()
	w.advance(2 * time.Second)
	w.pass()
	w.wait()
	accept := 0
	for _, text := range w.allWakeTexts() {
		if strings.Contains(text, "The person accepted the goal: goal check 2 (round 2)") && strings.Contains(text, "Call mission_finish") {
			accept++
			noBanned(t, "scripted accept wake", text)
		}
	}
	if accept != 1 || w.wakeRows() != wakesBefore+1 {
		t.Fatalf("accept wake-ups: %d, wake-ups %d->%d (%v)", accept, wakesBefore, w.wakeRows(), w.allWakeTexts())
	}
	// the lead takes the wake and calls mission_finish: the close proposal is FAKED here (w.propose writes the event). That wake ->
	// mission_finish -> GoalClose chain is covered only by the dynorg unit tests, not by this script.
	w.endCommandsDone()
	w.propose()
	judges = w.model.count()
	for i := 0; i < 8; i++ {
		w.pass()
		w.wait()
		w.endCommandsDone()
		w.advance(31 * time.Minute)
	}
	if w.wakeRows() != wakesBefore+1 || w.model.count() != judges || len(w.epochTriggers()) != epochs || w.checkRows() != 2 || rev.count() != 1 {
		t.Errorf("after the accept wake the loop spent: wake-ups %d, judge calls %d->%d, epochs %v, checks %d, reviewer %d", w.wakeRows(), judges, w.model.count(), w.epochTriggers(), w.checkRows(), rev.count())
	}
	if got := w.checks(); len(got) != 2 {
		t.Errorf("checks = %v", got)
	}
	var outcome, answeredBy string
	if err := w.st.db.QueryRow(`SELECT outcome FROM mission_goal_checks WHERE id = ?`, c.ID).Scan(&outcome); err != nil || outcome != "accepted" {
		t.Errorf("outcome %q %v", outcome, err)
	}
	if err := w.st.db.QueryRow(`SELECT answered_by FROM mission_goal_answers WHERE check_id = ?`, c.ID).Scan(&answeredBy); err != nil || answeredBy != "you" {
		t.Errorf("the accepting answer was by %q %v", answeredBy, err)
	}
}
