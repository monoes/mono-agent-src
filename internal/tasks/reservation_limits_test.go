package tasks

import (
	"errors"
	"testing"
	"time"
)

// A legacy session whose budget (set before the guard existed, here straight in the table) is above the guard
// is treated as a budget of the guard: reserve-all grants all that remains of that, and a second reserver gets
// the rest of the clamped budget only.
func TestLegacyBudgetAboveTheGuardIsClamped(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 2e12)
	first := rreserve(t, s, id, "a", 0.4e12)
	all := rreserve(t, s, id, "b", 0)
	if !near(first.AmountUSD, 0.4e12) || all.AmountUSD != 0.6e12 {
		t.Fatalf("first %v, reserve-all %v: want 0.4e12 and the remaining 0.6e12 of the clamped budget", first.AmountUSD, all.AmountUSD)
	}
	if _, err := s.Reserve(bg, "default", id, "c", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a third reserver on the exhausted clamped budget: %v", err)
	}
	c := rcommit(t, s, id)
	if left, bounded := c.Left(); !bounded || left > 1e-6 {
		t.Fatalf("left = %v %v", left, bounded)
	}
}

func TestReserveAllOfTheLargestBudgetWorks(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, MaxBudgetUSD)
	if r := rreserve(t, s, id, "a", 0); r.AmountUSD != MaxBudgetUSD {
		t.Fatalf("%+v", r)
	}
}

// One amount above the guard is refused by every entry point, with the row untouched.
func TestAnAmountAboveTheGuardIsRefused(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := s.Reserve(bg, "default", id, "x", "w", 2e12); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserve: %v", err)
	}
	r := rreserve(t, s, id, "a", 1)
	if err := s.Settle(bg, "default", r.ID, "a", 2e12); !errors.Is(err, ErrInvalid) {
		t.Fatalf("settle: %v", err)
	}
	if rrow(t, s, r.ID).State != ReservationLive {
		t.Fatal("the row is not live")
	}
}

// A cent is always representable below the guard: spent just under it plus a settled cent grows by that cent.
func TestSettleAtTheMarginKeepsTheCent(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 1e12)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, 1e12-0.01, id); err != nil {
		t.Fatal(err)
	}
	before := rspent(t, db, id)
	r := rreserve(t, s, id, "a", 0.01)
	if err := s.Settle(bg, "default", r.ID, "a", 0.01); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := rspent(t, db, id) - before; got < 0.0099 || got > 0.0101 {
		t.Fatalf("spent grew by %v, want 0.01", got)
	}
}

// A cost that would push spend past the guard, or that the sum cannot hold, is refused and the row stays live.
func TestSettleRefusesACostTheSpendCannotHold(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 1e12)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, 5e11, id); err != nil {
		t.Fatal(err)
	}
	r := rreserve(t, s, id, "a", 1)
	spent := rspent(t, db, id)
	for name, cost := range map[string]float64{"below the ulp of the spend": 1e-9} {
		if err := s.Settle(bg, "default", r.ID, "a", cost); !errors.Is(err, ErrLimit) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if rrow(t, s, r.ID).State != ReservationLive || rspent(t, db, id) != spent {
		t.Fatal("a refused settle changed the row or the spend")
	}
	// past the guard
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, 1e12-0.01, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 0.5); !errors.Is(err, ErrLimit) {
		t.Fatalf("past the guard: %v", err)
	}
	if rrow(t, s, r.ID).State != ReservationLive {
		t.Fatal("the row is not live")
	}
}

// The operator's resolve of an expired row is held to the same rule.
func TestResolveRefusesACostTheSpendCannotHold(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 1e12)
	r := rreserve(t, s, id, "a", 1)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, _ := s.ExpireStale(bg, "default", id); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, 1e12-0.01, id); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveReservation(bg, "default", r.ID, 0.5, "x", human); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	if rrow(t, s, r.ID).State != ReservationExpired {
		t.Fatal("the row ended")
	}
}

// No budget the store accepts can make a reservation fail.
func TestBudgetsAboveTheGuardAreRefused(t *testing.T) {
	s, _, _ := newTestStore(t)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "big", BudgetUSD: 1.5e12}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create: %v", err)
	}
	se := mustSession(t, s, "default", "ok")
	big := 1.5e12
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
