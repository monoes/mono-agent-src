package tasks

import (
	"context"
	"errors"
	"testing"
)

func TestOperatorSpendUnderHalfAMicroDollarIsAZeroCharge(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	se, err := s.AddSessionSpend(bg, "default", id, 4e-7, "tiny", human)
	if err != nil {
		t.Fatalf("a sub-micro amount is a zero charge, not a refusal: %v", err)
	}
	if se.SpentUSD != 0 || rspent(t, db, id) != 0 || eventCount(t, s, id, "spend") != 0 {
		t.Fatalf("spent %v, %d spend event(s): want a no-op", rspent(t, db, id), eventCount(t, s, id, "spend"))
	}
	if _, err := s.AddSessionSpend(bg, "default", id, 6e-7, "one", human); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, id); got != 1e-6 {
		t.Fatalf("6e-7 rounds to one micro-dollar, got %v", got)
	}
}

func TestOperatorSpendLandsExactlyOnTheCapAndNotPastIt(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0)
	setSpent(t, db, id, MaxBudgetUSD-1)
	if _, err := s.AddSessionSpend(bg, "default", id, 1.000001, "over", human); !errors.Is(err, ErrLimit) {
		t.Fatalf("past the cap: %v", err)
	}
	if rspent(t, db, id) != MaxBudgetUSD-1 || eventCount(t, s, id, "spend") != 0 {
		t.Fatal("a refused spend wrote something")
	}
	if _, err := s.AddSessionSpend(bg, "default", id, 1e300, "huge", human); !errors.Is(err, ErrLimit) {
		t.Fatalf("a huge amount: %v", err)
	}
	if _, err := s.AddSessionSpend(bg, "default", id, 1, "exact", human); err != nil {
		t.Fatalf("exactly at the cap: %v", err)
	}
	if rspent(t, db, id) != MaxBudgetUSD {
		t.Fatalf("spent = %v", rspent(t, db, id))
	}
}

func TestOperatorSpendSnapsALegacyOffGridTotal(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0)
	setSpent(t, db, id, 1.0000004)
	if _, err := s.AddSessionSpend(bg, "default", id, 1e-6, "n", human); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, id); got != 1.000001 {
		t.Fatalf("snapped total = %v, want 1.000001", got)
	}
}

func TestOperatorOverspendOfABudgetIsStillRecorded(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 1)
	if _, err := s.AddSessionSpend(bg, "default", id, 5, "over", human); err != nil {
		t.Fatalf("an overspend of a budget stays allowed: %v", err)
	}
	if rspent(t, db, id) != 5 || eventCount(t, s, id, "spend") != 1 {
		t.Fatal("not recorded")
	}
}

func TestChangeSessionSpendPastTheCapLeavesTheLimitsAlone(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	setSpent(t, db, id, MaxBudgetUSD)
	n := 9
	_, err := s.ChangeSession(bg, "default", id, SessionUpdate{MaxTasks: &n}, 1, "x", human)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	se, _ := s.GetSession(bg, "default", id, human)
	if se.MaxTasks == 9 || eventCount(t, s, id, "limits") != 0 {
		t.Fatal("the limits changed with a refused spend")
	}
	setSpent(t, db, id, 0)
	if _, err := s.ChangeSession(bg, "default", id, SessionUpdate{MaxTasks: &n}, 3e-7, "x", human); err != nil {
		t.Fatal(err)
	}
	se, _ = s.GetSession(bg, "default", id, human)
	if se.MaxTasks != 9 || se.SpentUSD != 0 || eventCount(t, s, id, "spend") != 0 {
		t.Fatalf("limits apply, the sub-micro spend is a no-op: %+v", se)
	}
}

func TestSpendWithRunsItsRecordEvenForAZeroCharge(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0)
	ran := 0
	if _, err := s.AddSessionSpendWith(bg, "default", id, 2e-7, "n", human, func(_ context.Context, _ Tx) error { ran++; return nil }); err != nil {
		t.Fatal(err)
	}
	if ran != 1 || rspent(t, db, id) != 0 {
		t.Fatalf("record ran %d time(s), spent %v: the caller's once-only record must still be written", ran, rspent(t, db, id))
	}
	setSpent(t, db, id, MaxBudgetUSD)
	if _, err := s.AddSessionSpendWith(bg, "default", id, 1, "n", human, func(_ context.Context, _ Tx) error { ran++; return nil }); !errors.Is(err, ErrLimit) || ran != 1 {
		t.Fatalf("past the cap must refuse before the record: %v ran %d", err, ran)
	}
}

func sessionRev(t *testing.T, db dbx, id int64) int64 {
	t.Helper()
	var rev int64
	err := db.QueryRowContext(bg, `SELECT COALESCE((SELECT rev FROM task_session_rev WHERE session_id = ?), 0)`, id).Scan(&rev)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestAZeroChargeLeavesTheSessionRevisionAlone(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	before := sessionRev(t, db, id)
	ran := false
	if _, err := s.AddSessionSpendWith(bg, "default", id, 4e-7, "tiny", human, func(context.Context, Tx) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("zero charge: err %v, record ran %v (it must still run)", err, ran)
	}
	if _, err := s.AddSessionSpend(bg, "default", id, 4e-7, "tiny", human); err != nil {
		t.Fatal(err)
	}
	if got := sessionRev(t, db, id); got != before {
		t.Fatalf("a zero charge moved the revision %d -> %d: an approval guarded on it would be refused", before, got)
	}
	if _, err := s.AddSessionSpend(bg, "default", id, 1, "real", human); err != nil {
		t.Fatal(err)
	}
	if got := sessionRev(t, db, id); got == before {
		t.Fatal("a real charge must move the revision")
	}
}
