package tasks

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func setBudget(s *Store, id int64, b float64) (Session, error) {
	return s.UpdateSession(bg, "default", id, SessionUpdate{BudgetUSD: &b}, human)
}

func budgetNow(t *testing.T, s *Store, id int64) float64 {
	t.Helper()
	se, err := s.GetSession(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	return se.BudgetUSD
}

func eventCount(t *testing.T, s *Store, id int64, kind string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND kind = ?`, id, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantFloorRefusal(t *testing.T, err error, parts ...string) {
	t.Helper()
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("want a limit refusal, got %v", err)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("the refusal %q does not show %q", err, p)
		}
	}
}

func TestSetBudgetBelowWhatIsSpentIsRefusedAndAtItIsAllowed(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 4 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	maxTasks := 7
	b := 3.0
	_, err := s.UpdateSession(bg, "default", id, SessionUpdate{BudgetUSD: &b, MaxTasks: &maxTasks}, human)
	wantFloorRefusal(t, err, "spent $4.0000", "reservations $0.0000", "children $0.0000")
	se, _ := s.GetSession(bg, "default", id, human)
	if se.BudgetUSD != 10 || se.MaxTasks == 7 {
		t.Fatalf("a refused floor changed the session: %+v", se)
	}
	if n := eventCount(t, s, id, "limits"); n != 0 {
		t.Fatalf("a refused floor wrote %d limits event(s)", n)
	}
	if _, err := setBudget(s, id, 4); err != nil {
		t.Fatalf("a budget equal to the commitment: %v", err)
	}
}

func TestSetBudgetBelowSpentPlusReservationsIsRefusedUntilSettled(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 2 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	r := rreserve(t, s, id, "a", 3)
	_, err := setBudget(s, id, 4)
	wantFloorRefusal(t, err, "spent $2.0000", "reservations $3.0000")
	if budgetNow(t, s, id) != 10 {
		t.Fatal("the budget moved")
	}
	if _, err := setBudget(s, id, 5); err != nil {
		t.Fatalf("at spent + reservations: %v", err)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := setBudget(s, id, 3); err != nil {
		t.Fatalf("after the settle only the spent $3 is committed: %v", err)
	}
}

func TestSetBudgetBelowWhatTheChildrenHaveCommittedIsRefused(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 20)
	a := rkid(t, s, db, root, 6, "running")
	b := rkid(t, s, db, root, 3, "running")
	grand := rkid(t, s, db, a, 4, "running")
	setSpent(t, db, a, 2)
	setSpent(t, db, grand, 1)
	rreserve(t, s, b, "p", 1)
	// committed below root: a 2 + grand 1 + b's reservation 1 = 4; the unspent allowances (6, 3, 4) do not count
	_, err := setBudget(s, root, 3.5)
	wantFloorRefusal(t, err, "children $4.0000")
	if budgetNow(t, s, root) != 20 {
		t.Fatal("the budget moved")
	}
	if _, err := setBudget(s, root, 4); err != nil {
		t.Fatalf("at what the children committed: %v", err)
	}
	if budgetNow(t, s, a) != 4 || budgetNow(t, s, b) != 3 {
		t.Fatal("lowering the parent still clamps the children's allowances")
	}
}

func TestSetBudgetRefusesWhileAnUnboundedWorkerIsLiveOrExpired(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 0)
	free := rreserve(t, s, id, "p1", 0)
	if free.Bounded {
		t.Fatal("setup: the row should be unbounded")
	}
	_, err := setBudget(s, id, 5)
	wantFloorRefusal(t, err, "worker-p1", "live", "#"+itoa(id))
	if budgetNow(t, s, id) != 0 {
		t.Fatal("the budget moved")
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.ExpireStale(bg, "default", 0); err != nil {
		t.Fatal(err)
	}
	_, err = setBudget(s, id, 5)
	wantFloorRefusal(t, err, "worker-p1", "expired")
	// the operator charges what the lost worker cost: the row no longer holds
	if err := s.ResolveReservation(bg, "default", free.ID, 1, "n", human); err != nil {
		t.Fatal(err)
	}
	if _, err := setBudget(s, id, 5); err != nil {
		t.Fatalf("after the resolve: %v", err)
	}
}

func TestSetBudgetAllowedAfterAnUnboundedWorkerSettles(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0)
	r := rreserve(t, s, id, "p1", 0)
	if _, err := setBudget(s, id, 5); err == nil {
		t.Fatal("refused expected while live")
	}
	if err := s.Settle(bg, "default", r.ID, "p1", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := setBudget(s, id, 5); err != nil {
		t.Fatalf("after settle: %v", err)
	}
	if _, err := setBudget(s, id, 1); err == nil {
		t.Fatal("$2 was spent: a budget of $1 is below the floor")
	}
}

func TestAnUnboundedWorkerBelowTheSessionBlocksAsWell(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 0)
	kid := rkid(t, s, db, root, 0, "running")
	rreserve(t, s, kid, "p1", 0)
	_, err := setBudget(s, root, 5)
	wantFloorRefusal(t, err, "worker-p1", "#"+itoa(kid))
}

func TestSetBudgetZeroRemovesTheBudgetEvenBelowTheCommitment(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 4 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	rreserve(t, s, id, "a", 3)
	if _, err := setBudget(s, id, 0); err != nil {
		t.Fatalf("B=0 removes the cap: %v", err)
	}
	// and under a budgeted ancestor it is still refused, by the parent rule
	root := rsession(t, s, db, 10)
	kid := rkid(t, s, db, root, 5, "running")
	if _, err := setBudget(s, kid, 0); err == nil {
		t.Fatal("B=0 under a budgeted ancestor must stay refused")
	}
}

func TestSetBudgetUnchangedIsNotAFloorQuestion(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 12 WHERE id = ?`, id); err != nil { // an overspend recorded earlier
		t.Fatal(err)
	}
	if _, err := setBudget(s, id, 10); err != nil {
		t.Fatalf("restating the budget: %v", err)
	}
}
