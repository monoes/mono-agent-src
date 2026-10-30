package tasks

import (
	"errors"
	"testing"
)

// A valid budget above the old amount cap still reserves all (the amount cap is only an overflow guard, and
// no accepted budget exceeds it).
func TestReserveAllOfALargeBudgetAndSettleALargeCost(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 2e12)
	r := rreserve(t, s, id, "a", 0)
	if !near(r.AmountUSD, 2e12) || !r.Bounded {
		t.Fatalf("%+v", r)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 1.5e12); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, id); got != 1.5e12 {
		t.Fatalf("spent = %v", got)
	}
	// a cost above the guard is still refused
	r2 := rreserve(t, s, id, "b", 1)
	if err := s.Settle(bg, "default", r2.ID, "b", MaxBudgetUSD*2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}

func TestReserveAllOfTheLargestBudgetWorks(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, MaxBudgetUSD)
	if r := rreserve(t, s, id, "a", 0); r.AmountUSD != MaxBudgetUSD {
		t.Fatalf("%+v", r)
	}
}

// No budget the store accepts can make a reservation fail.
func TestBudgetsAboveTheGuardAreRefused(t *testing.T) {
	s, _, _ := newTestStore(t)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "big", BudgetUSD: MaxBudgetUSD * 10}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create: %v", err)
	}
	se := mustSession(t, s, "default", "ok")
	big := MaxBudgetUSD * 10
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &big}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update: %v", err)
	}
	ok := MaxBudgetUSD
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &ok}, human); err != nil {
		t.Fatalf("update to the guard: %v", err)
	}
}

// A settle names the profile on every step: another profile's settle with the right owner changes nothing of
// this profile's row or spend, and nothing of its own.
func TestSettleAcrossProfilesChangesNothing(t *testing.T) {
	s, db, _ := newTestStore(t)
	addProfile(t, db, "other")
	a := rsession(t, s, db, 10)
	r := rreserve(t, s, a, "owner", 4)
	osess := mustSession(t, s, "other", "o")
	if _, err := db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, osess.ID); err != nil {
		t.Fatal(err)
	}
	before := rrow(t, s, r.ID)
	for _, fn := range []func() error{
		func() error { return s.Settle(bg, "other", r.ID, "owner", 3) },
		func() error { return s.ReleaseReservation(bg, "other", r.ID, "owner") },
		func() error { return s.Heartbeat(bg, "other", r.ID, "owner") },
	} {
		if err := fn(); !errors.Is(err, ErrReservationLost) {
			t.Fatalf("cross-profile call: %v", err)
		}
	}
	if after := rrow(t, s, r.ID); after != before || rspent(t, db, a) != 0 || rspent(t, db, osess.ID) != 0 {
		t.Fatalf("changed: %+v", after)
	}
}
