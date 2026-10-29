package tasks

import (
	"errors"
	"testing"
)

// runningParent is a running session with a budget and one task, and its agent.
func runningParent(t *testing.T, s *Store, budget float64) (Session, Actor) {
	t.Helper()
	p, lead := carveParent(t, s, budget)
	mustTask(t, s, "default", p.ID, "work")
	mustTask(t, s, "default", p.ID, "more work")
	return mustRun(t, s, "default", p), lead
}

func leftOf(t *testing.T, s *Store, id int64) float64 {
	t.Helper()
	w := readWorld(t, s)
	return w.by[id].BudgetUSD - w.commit(id)
}

// A $10 parent whose child holds $9 can issue a worker only the $1 that is left.
func TestAParentsWorkerIsIssuedWhatIsLeftAfterItsChildren(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := runningParent(t, s, 10)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 9}, lead); err != nil {
		t.Fatal(err)
	}
	g, err := s.ReserveSpend(bg, "default", p.ID, 0)
	if err != nil || g < 0.99 || g > 1.01 {
		t.Fatalf("granted %v, %v: want the $1 left", g, err)
	}
	if _, err := s.ReserveSpend(bg, "default", p.ID, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a second worker was issued money that is reserved: %v", err)
	}
}

// A worker reserved first: a child is carved only from what is left of the reservation.
func TestAChildIsNotCarvedOutOfARunningWorkersReservation(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := runningParent(t, s, 10)
	if g, err := s.ReserveSpend(bg, "default", p.ID, 0); err != nil || g != 10 {
		t.Fatalf("reserve: %v %v", g, err)
	}
	if k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 10}, lead); !errors.Is(err, ErrLimit) {
		t.Fatalf("a child took the running worker's $10: %+v %v", k, err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead); !errors.Is(err, ErrLimit) {
		t.Fatalf("a child got a share of a fully reserved budget: %v", err)
	}
	// the claim on a session with nothing left is refused as well
	if _, err := s.SessionNext(bg, "default", p.ID, Actor{Kind: Agent, Name: "w2", Session: p.ID}, true, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("dispatch admitted work on a fully reserved session: %v", err)
	}
}

// An open child that spent more than its budget holds what it spent.
func TestAnOpenChildHoldsAtLeastWhatItSpent(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := runningParent(t, s, 10)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 4}, lead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", k.ID, 8, "work", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "second", ParentSessionID: p.ID, BudgetUSD: 6}, lead); !errors.Is(err, ErrLimit) {
		t.Fatalf("a $6 child was accepted beside a child that spent $8 of a $10 parent: %v", err)
	}
}

// An update cannot lower a child below what it spent, reserved or lent its children, or make it unlimited.
func TestAChildsBudgetIsNotLoweredBelowItsCommitmentOrUnlimited(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := runningParent(t, s, 10)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 8}, lead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", k.ID, 6, "work", human); err != nil {
		t.Fatal(err)
	}
	low, zero := 2.0, 0.0
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: &low}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("lowered below its spend: %v", err)
	}
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: &zero}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("made unlimited under a capped parent: %v", err)
	}
	if got := readWorld(t, s).by[k.ID].BudgetUSD; got != 8 {
		t.Fatalf("the refused updates changed the budget to %v", got)
	}
}

// An overspend is recorded as it happened and blocks every later reserve, carve and dispatch.
func TestAnOverspendIsRecordedAndBlocksLaterAdmissions(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := runningParent(t, s, 10)
	g, err := s.ReserveSpend(bg, "default", p.ID, 4)
	if err != nil || g != 4 {
		t.Fatalf("reserve: %v %v", g, err)
	}
	if err := s.SettleSpend(bg, "default", p.ID, 4, 11, "work"); err != nil {
		t.Fatal(err)
	}
	got := readWorld(t, s).by[p.ID]
	if got.SpentUSD != 11 || got.ReservedUSD != 0 {
		t.Fatalf("spent %v reserved %v: want 11 and 0", got.SpentUSD, got.ReservedUSD)
	}
	if _, err := s.ReserveSpend(bg, "default", p.ID, 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("reserve after an overspend: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 1}, lead); !errors.Is(err, ErrLimit) {
		t.Fatalf("carve after an overspend: %v", err)
	}
	if _, err := s.SessionNext(bg, "default", p.ID, Actor{Kind: Agent, Name: "w", Session: p.ID}, true, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("dispatch after an overspend: %v", err)
	}
}

// Settling gives the unused part of a reservation back; a released reservation (a crashed worker) too.
func TestSettlingAndReleasingGiveReservationsBack(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, _ := runningParent(t, s, 10)
	if _, err := s.ReserveSpend(bg, "default", p.ID, 6); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleSpend(bg, "default", p.ID, 6, 2, "work"); err != nil {
		t.Fatal(err)
	}
	if l := leftOf(t, s, p.ID); l < 7.99 || l > 8.01 {
		t.Fatalf("left %v after settling $2 of $6: want 8", l)
	}
	if _, err := s.ReserveSpend(bg, "default", p.ID, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReserved(bg, "default", p.ID); err != nil {
		t.Fatal(err)
	}
	if l := leftOf(t, s, p.ID); l < 7.99 || l > 8.01 {
		t.Fatalf("left %v after releasing a crashed worker's $5: want 8", l)
	}
	// a late settle of the released reservation cannot make the reservation negative
	if err := s.SettleSpend(bg, "default", p.ID, 5, 0, ""); err != nil {
		t.Fatal(err)
	}
	if got := readWorld(t, s).by[p.ID].ReservedUSD; got != 0 {
		t.Fatalf("reserved %v", got)
	}
}

// A closed session takes no new child and its children's budgets are not raised: its hold on its own
// parent is what it is committed to, which a carve or a raise would grow unchecked.
func TestAClosedSessionTakesNoChildAndNoRaise(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := runningParent(t, s, 10)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "mid", ParentSessionID: p.ID, BudgetUSD: 4}, lead)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateSession(bg, "default", SessionInput{Title: "leaf", ParentSessionID: k.ID, BudgetUSD: 1}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopSession(bg, "default", k.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "late", ParentSessionID: k.ID, BudgetUSD: 1}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("a stopped session took a child: %v", err)
	}
	more := 3.0
	if _, err := s.UpdateSession(bg, "default", g.ID, SessionUpdate{BudgetUSD: &more}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("a child of a stopped session was raised: %v", err)
	}
	if _, err := s.ReserveSpend(bg, "default", k.ID, 1); err == nil {
		t.Fatal("a stopped session issued a worker budget")
	}
}
