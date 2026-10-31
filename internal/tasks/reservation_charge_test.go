package tasks

import (
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"
)

func rcharged(t *testing.T, s *Store, id int64) float64 { return rrow(t, s, id).ChargedUSD }

func setSpent(t *testing.T, db *sql.DB, session int64, usd float64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, usd, session); err != nil {
		t.Fatal(err)
	}
}

func TestChargeGrowsTheChargeAndTheSpendByTheDelta(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "a", 5)
	for _, step := range []struct{ cum, wantSpent float64 }{{1, 1}, {1.5, 1.5}, {1.5, 1.5}, {1, 1.5}, {0, 1.5}, {3.25, 3.25}} {
		if err := s.ChargeReservation(bg, "default", r.ID, "a", step.cum); err != nil {
			t.Fatalf("charge %v: %v", step.cum, err)
		}
		if got := rspent(t, db, sid); got != step.wantSpent {
			t.Fatalf("after cumulative %v: spent %v, want %v", step.cum, got, step.wantSpent)
		}
		if got := rcharged(t, s, r.ID); got != step.wantSpent {
			t.Fatalf("after cumulative %v: charged %v, want %v", step.cum, got, step.wantSpent)
		}
	}
	if c := rcommit(t, s, sid); !near(c.Spent, 3.25) || !near(c.Held, 1.75) {
		t.Fatalf("%+v", c)
	}
}

func TestSettleAfterChargesAddsOnlyTheRemainder(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "a", 5)
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 3); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, sid); got != 3 {
		t.Fatalf("spent %v, a cost of 3 counted once", got)
	}
}

// A cost beyond the reserved amount is recorded as it happened (as Settle does): it is overspend, never a refusal.
func TestChargeBeyondTheReservedAmountIsRecordedOverspend(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "a", 2)
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 3.5); err != nil {
		t.Fatal(err)
	}
	c := rcommit(t, s, sid)
	if !near(c.Spent, 3.5) || c.Held != 0 {
		t.Fatalf("%+v", c)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 3.5); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, sid); got != 3.5 {
		t.Fatalf("spent %v", got)
	}
}

// Only the owner's live row is charged; every other caller changes nothing and is told the reservation is lost.
func TestChargeByAnyoneButTheOwnerOfALiveRowChangesNothing(t *testing.T) {
	s, db, clk := newTestStore(t)
	addProfile(t, db, "other")
	sid := rsession(t, s, db, 10)
	live := rreserve(t, s, sid, "a", 5)
	settled := rreserve(t, s, sid, "a", 1)
	released := rreserve(t, s, sid, "a", 1)
	expired := rreserve(t, s, sid, "gone", 1)
	if err := s.Settle(bg, "default", settled.ID, "a", 0.5); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReservation(bg, "default", released.ID, "a"); err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if err := s.Heartbeat(bg, "default", live.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireStale(bg, "default", sid); err != nil || n != 1 {
		t.Fatalf("%d, %v", n, err)
	}
	spent := rspent(t, db, sid)
	cases := []struct {
		name    string
		profile string
		id      int64
		owner   string
	}{
		{"stranger", "default", live.ID, "stranger"},
		{"empty owner", "default", live.ID, ""},
		{"other profile", "other", live.ID, "a"},
		{"settled row", "default", settled.ID, "a"},
		{"released row", "default", released.ID, "a"},
		{"expired row by its owner", "default", expired.ID, "gone"},
		{"unknown id", "default", 99999, "a"},
	}
	for _, c := range cases {
		before := []Reservation{rrow(t, s, live.ID), rrow(t, s, settled.ID), rrow(t, s, released.ID), rrow(t, s, expired.ID)}
		if err := s.ChargeReservation(bg, c.profile, c.id, c.owner, 4); !errors.Is(err, ErrReservationLost) {
			t.Fatalf("%s: %v", c.name, err)
		}
		after := []Reservation{rrow(t, s, live.ID), rrow(t, s, settled.ID), rrow(t, s, released.ID), rrow(t, s, expired.ID)}
		for i := range before {
			if before[i] != after[i] {
				t.Fatalf("%s: row %d changed: %+v -> %+v", c.name, i, before[i], after[i])
			}
		}
		if got := rspent(t, db, sid); got != spent {
			t.Fatalf("%s: spent moved %v -> %v", c.name, spent, got)
		}
	}
}

// The same rule as Heartbeat: a lease that lapsed but was not swept is still the owner's.
func TestChargeRenewsNothingButWorksOnALapsedUnsweptRow(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "a", 5)
	clk.advance(ReservationLease + 10*time.Hour)
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 1); err != nil {
		t.Fatal(err)
	}
	if got := rrow(t, s, r.ID); got.LeaseUntil != r.LeaseUntil || got.ChargedUSD != 1 {
		t.Fatalf("%+v", got)
	}
}

// Crash model: the worker charges, its handle is dropped, a sweep expires the row, and the row still holds
// what was not charged while what was charged stays spent.
func TestChargeThenCrashKeepsTheChargeAndTheHold(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "dies", 6)
	if err := s.ChargeReservation(bg, "default", r.ID, "dies", 2.5); err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, err := s.ExpireStale(bg, "default", sid); err != nil || n != 1 {
		t.Fatalf("%d, %v", n, err)
	}
	got := rrow(t, s, r.ID)
	if got.State != ReservationExpired || got.ChargedUSD != 2.5 {
		t.Fatalf("%+v", got)
	}
	if c := rcommit(t, s, sid); !near(c.Spent, 2.5) || !near(c.Held, 3.5) {
		t.Fatalf("%+v", c)
	}
	if err := s.ChargeReservation(bg, "default", r.ID, "dies", 4); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("an expired row is charged only by the operator: %v", err)
	}
	if err := s.ResolveReservation(bg, "default", r.ID, 4, "", human); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, sid); got != 4 {
		t.Fatalf("spent %v, the 2.5 charged counted once", got)
	}
}

func TestChargeIsExactNearTheCapAndRefusesWithARollback(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 0)
	setSpent(t, db, sid, MaxBudgetUSD-0.000001)
	r := rreserve(t, s, sid, "a", 5)
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 0.000001); err != nil {
		t.Fatal(err)
	}
	if got := rspent(t, db, sid); got != MaxBudgetUSD {
		t.Fatalf("spent %v", got)
	}
	before := rrow(t, s, r.ID)
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 0.000002); !errors.Is(err, ErrLimit) {
		t.Fatalf("a micro-dollar past the cap: %v", err)
	}
	if after := rrow(t, s, r.ID); after != before || rspent(t, db, sid) != MaxBudgetUSD {
		t.Fatalf("not rolled back: %+v vs %+v, spent %v", after, before, rspent(t, db, sid))
	}
}

func TestChargeOfASmallerStepThanTheCapWorksFromAnOffGridSpend(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 0)
	setSpent(t, db, sid, 99999999.123457)
	r := rreserve(t, s, sid, "a", 5)
	for i := 1; i <= 3; i++ {
		if err := s.ChargeReservation(bg, "default", r.ID, "a", float64(i)*0.000001); err != nil {
			t.Fatal(err)
		}
	}
	if got := toMicros(rspent(t, db, sid)); got != 99999999123457+3 {
		t.Fatalf("spent %d micros", got)
	}
}

func TestAChargeUnderHalfAMicroDollarIsAZeroChargeNotARefusal(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "a", 5)
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 0.0000004); err != nil {
		t.Fatal(err)
	}
	if rspent(t, db, sid) != 0 || rcharged(t, s, r.ID) != 0 {
		t.Fatal("a sub-quantum cost moved money")
	}
	if err := s.ChargeReservation(bg, "default", r.ID, "a", 0.0000006); err != nil {
		t.Fatal(err)
	}
	if toMicros(rspent(t, db, sid)) != 1 || toMicros(rcharged(t, s, r.ID)) != 1 {
		t.Fatalf("spent %v charged %v", rspent(t, db, sid), rcharged(t, s, r.ID))
	}
}

func TestChargeRefusesBadAmounts(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r := rreserve(t, s, sid, "a", 5)
	for _, v := range []float64{-1, MaxBudgetUSD + 1, math.NaN(), math.Inf(1)} {
		if err := s.ChargeReservation(bg, "default", r.ID, "a", v); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v: %v", v, err)
		}
	}
	if rspent(t, db, sid) != 0 {
		t.Fatal("spent moved")
	}
}
