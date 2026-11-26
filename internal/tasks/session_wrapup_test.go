package tasks

import (
	"errors"
	"testing"
)

// spentMission is a running session of budget $1 that spent it, with one task the lead holds.
func spentMission(t *testing.T, s *Store, budget, spend float64) Session {
	t.Helper()
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "M", Goal: "g", BudgetUSD: budget}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "T", Acceptance: "done"}, human); err != nil {
		t.Fatal(err)
	}
	if se, err = s.ApproveSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if spend > 0 {
		if se, err = s.AddSessionSpend(bg, "default", se.ID, spend, "x", human); err != nil {
			t.Fatal(err)
		}
	}
	return se
}

var conductorActor = func(id int64) Actor { return Actor{Kind: Agent, Name: ConductorName, Session: id} }

func wrapUpOf(t *testing.T, s *Store, id int64) *WrapUp {
	t.Helper()
	w, err := s.WrapUpOf(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func wantWrapUp(t *testing.T, w *WrapUp, spent, held, left float64) {
	t.Helper()
	if w == nil {
		t.Fatal("no wrap-up allowance")
	}
	if w.AllowanceUSD != 5 || w.SpentUSD != spent || w.HeldUSD != held || w.LeftUSD != left {
		t.Fatalf("wrap-up %+v, want spent %v held %v left %v of $5", *w, spent, held, left)
	}
}

// The allowance exists only for a mission with a budget that it has spent, while it runs or after the conductor's
// budget halt: never without a budget, with money left, after the operator's pause or stop, or in a draft.
func TestWrapUpAppliesOnlyToExhaustionByBudget(t *testing.T) {
	s, _, _ := newTestStore(t)
	if w := wrapUpOf(t, s, spentMission(t, s, 0, 40).ID); w != nil {
		t.Errorf("an unlimited budget never exhausts: %+v", *w)
	}
	if w := wrapUpOf(t, s, spentMission(t, s, 1, 0.999999).ID); w != nil {
		t.Errorf("money is left: %+v", *w)
	}

	running := spentMission(t, s, 1, 1)
	wantWrapUp(t, wrapUpOf(t, s, running.ID), 0, 0, 5)
	if _, err := s.PauseSession(bg, "default", running.ID, conductorActor(running.ID)); err != nil {
		t.Fatal(err)
	}
	wantWrapUp(t, wrapUpOf(t, s, running.ID), 0, 0, 5)

	paused := spentMission(t, s, 1, 0.5)
	if _, err := s.PauseSession(bg, "default", paused.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", paused.ID, 1, "in flight", human); err != nil {
		t.Fatal(err)
	}
	if w := wrapUpOf(t, s, paused.ID); w != nil {
		t.Errorf("the operator's pause grants nothing: %+v", *w)
	}
	// a halt, then the operator resumes without raising and pauses: the operator's pause again
	if _, err := s.ResumeSession(bg, "default", running.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseSession(bg, "default", running.ID, human); err != nil {
		t.Fatal(err)
	}
	if w := wrapUpOf(t, s, running.ID); w != nil {
		t.Errorf("paused by the operator after the halt: %+v", *w)
	}

	stopped := spentMission(t, s, 1, 2)
	if _, err := s.StopSession(bg, "default", stopped.ID, human); err != nil {
		t.Fatal(err)
	}
	if w := wrapUpOf(t, s, stopped.ID); w != nil {
		t.Errorf("a stopped mission: %+v", *w)
	}
	draft, err := s.CreateSession(bg, "default", SessionInput{Title: "D", Goal: "g", BudgetUSD: 1}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", draft.ID, 2, "x", human); err != nil {
		t.Fatal(err)
	}
	if w := wrapUpOf(t, s, draft.ID); w != nil {
		t.Errorf("a draft: %+v", *w)
	}
}

// A turn holds what is left until its cost is known, so a turn that dies or reports nothing keeps the hold and a
// second turn cannot be given the same money. Its cost goes on the meter and against the allowance, once; the
// turn after the allowance is used is refused; raising the budget gives a new allowance.
func TestWrapUpTurnsAreHeldChargedAndBounded(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := spentMission(t, s, 1, 1.5)
	begin := func(turn string) (*WrapUp, error) { return s.BeginWrapUpTurn(bg, "default", se.ID, turn, human) }

	w, err := begin("t1")
	if err != nil {
		t.Fatal(err)
	}
	wantWrapUp(t, w, 0, 0, 5) // what this turn may use
	wantWrapUp(t, wrapUpOf(t, s, se.ID), 0, 5, 0)
	if _, err := begin("t1b"); !errors.Is(err, ErrWrapUpUsedUp) {
		t.Fatalf("a second turn while one holds the allowance: %v", err)
	}
	for range 2 { // settled once, however often it is reported
		if err := s.SettleWrapUpTurn(bg, "default", se.ID, "t1", 2, human); err != nil {
			t.Fatal(err)
		}
	}
	wantWrapUp(t, wrapUpOf(t, s, se.ID), 2, 0, 3)
	if _, spent := budgetOf(t, s, se.ID); spent != 3.5 {
		t.Fatalf("the meter says $%v, want $3.5", spent)
	}

	// a turn that never ran gives its hold back
	if _, err := begin("t2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DropWrapUpTurn(bg, "default", se.ID, "t2", human); err != nil {
		t.Fatal(err)
	}
	wantWrapUp(t, wrapUpOf(t, s, se.ID), 2, 0, 3)

	// a turn may overshoot: the next one is refused
	if _, err := begin("t3"); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleWrapUpTurn(bg, "default", se.ID, "t3", 3.25, human); err != nil {
		t.Fatal(err)
	}
	wantWrapUp(t, wrapUpOf(t, s, se.ID), 5.25, 0, 0)
	if w, err := begin("t4"); !errors.Is(err, ErrWrapUpUsedUp) || w == nil || w.SpentUSD != 5.25 {
		t.Fatalf("after the allowance: %+v %v", w, err)
	}
	// a settled turn cannot be dropped back into the allowance
	if err := s.DropWrapUpTurn(bg, "default", se.ID, "t3", human); err != nil {
		t.Fatal(err)
	}
	wantWrapUp(t, wrapUpOf(t, s, se.ID), 5.25, 0, 0)
	// the same budget set again is not a raise
	same, more := 1.0, 20.0
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &same}, human); err == nil {
		if _, err := begin("t5"); !errors.Is(err, ErrWrapUpUsedUp) {
			t.Fatalf("after restating the budget: %v", err)
		}
	}
	// raised: no longer exhausted, and the next exhaustion has the whole allowance
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &more}, human); err != nil {
		t.Fatal(err)
	}
	if w, err := begin("t6"); err != nil || w != nil {
		t.Fatalf("a raised budget: %+v %v", w, err)
	}
	if _, err := s.AddSessionSpend(bg, "default", se.ID, 20, "x", human); err != nil {
		t.Fatal(err)
	}
	wantWrapUp(t, wrapUpOf(t, s, se.ID), 0, 0, 5)
}

// A turn whose mission was stopped or closed meanwhile is settled but not charged, as any lead turn.
func TestWrapUpSettleDoesNotChargeAStoppedMission(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := spentMission(t, s, 1, 1)
	if _, err := s.BeginWrapUpTurn(bg, "default", se.ID, "t1", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleWrapUpTurn(bg, "default", se.ID, "t1", 2, human); err != nil {
		t.Fatal(err)
	}
	if _, spent := budgetOf(t, s, se.ID); spent != 1 {
		t.Fatalf("a stopped mission was charged: $%v", spent)
	}
	for _, op := range []error{
		s.SettleWrapUpTurn(bg, "default", se.ID, "t1", 1, bot("a")),
		s.DropWrapUpTurn(bg, "default", se.ID, "t1", bot("a")),
		func() error { _, err := s.BeginWrapUpTurn(bg, "default", se.ID, "t9", bot("a")); return err }(),
	} {
		if !errors.Is(op, ErrOperatorOnly) {
			t.Errorf("an agent moved the allowance: %v", op)
		}
	}
}

// The lead hands back the task it holds while its budget-halted mission has allowance left; without the
// conductor's word, after the operator's pause, or once the allowance is used, a paused session takes nothing.
func TestWrapUpLetsTheLeadHandBackOnAHaltedMission(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := spentMission(t, s, 1, 0)
	ts, err := s.SessionTasks(bg, "default", se.ID, Filter{}, human)
	if err != nil || len(ts) != 1 {
		t.Fatalf("tasks: %v %v", ts, err)
	}
	lead := Actor{Kind: Agent, Name: "lead", Session: se.ID, Task: ts[0].ID}
	if _, err := s.SessionClaim(bg, "default", se.ID, ts[0].ID, lead, DefaultLease); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", se.ID, 1, "x", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseSession(bg, "default", se.ID, conductorActor(se.ID)); err != nil {
		t.Fatal(err)
	}
	artifact := ArtifactInput{Kind: "test", Ref: "go test ok"}
	if _, err := s.AddArtifact(bg, "default", se.ID, ts[0].ID, artifact, lead); !errors.Is(err, ErrNotReady) {
		t.Fatalf("without the wrap-up: %v", err)
	}
	wrap := lead
	wrap.WrapUp = true
	if _, err := s.AddArtifact(bg, "default", se.ID, ts[0].ID, artifact, wrap); err != nil {
		t.Fatalf("evidence under the allowance: %v", err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "done"}, lead); !errors.Is(err, ErrNotReady) {
		t.Fatalf("finish without the wrap-up: %v", err)
	}
	// the store checks for itself: a flag alone does not open an operator's pause
	if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "done"}, wrap); !errors.Is(err, ErrNotReady) {
		t.Fatalf("finish after the operator's pause: %v", err)
	}
	if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PauseSession(bg, "default", se.ID, conductorActor(se.ID)); err != nil {
		t.Fatal(err)
	}
	// used up
	if _, err := s.BeginWrapUpTurn(bg, "default", se.ID, "t1", human); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleWrapUpTurn(bg, "default", se.ID, "t1", 5, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "done"}, wrap); !errors.Is(err, ErrNotReady) {
		t.Fatalf("finish after the allowance: %v", err)
	}
	// a new allowance after a raise and the next halt
	more := 10.0
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &more}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", se.ID, 5, "x", human); err != nil {
		t.Fatal(err)
	}
	done, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "done"}, wrap)
	if err != nil || done.Status != StatusReview {
		t.Fatalf("finish under the new allowance: %v %v", done.Status, err)
	}
}
