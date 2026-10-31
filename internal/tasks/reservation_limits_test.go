package tasks

import (
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"
)

// The money quantum is a millionth of a dollar: every cost is rounded to it first, the rounded value is what the
// reservation records AND what the spend grows by, and the spend is kept as a whole number of quanta, so nothing
// is created or lost by a float64 add.
func micros(x float64) int64 { return int64(math.Round(x * 1e6)) }

// A legacy session whose budget (set before the guard existed, here straight in the table) is above the guard
// is treated as a budget of the guard: reserve-all grants all that remains of that, and a second reserver gets
// the rest of the clamped budget only.
func TestLegacyBudgetAboveTheGuardIsClamped(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 2e9)
	first := rreserve(t, s, id, "a", 0.4e9)
	all := rreserve(t, s, id, "b", 0)
	if !near(first.AmountUSD, 0.4e9) || all.AmountUSD != 0.6e9 {
		t.Fatalf("first %v, reserve-all %v: want 0.4e9 and the remaining 0.6e9 of the clamped budget", first.AmountUSD, all.AmountUSD)
	}
	if _, err := s.Reserve(bg, "default", id, "c", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a third reserver on the exhausted clamped budget: %v", err)
	}
}

func TestReserveAllOfTheLargestBudgetWorks(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, MaxBudgetUSD)
	if r := rreserve(t, s, id, "a", 0); r.AmountUSD != MaxBudgetUSD {
		t.Fatalf("%+v", r)
	}
}

func TestTheGuardIsABillionDollars(t *testing.T) {
	if MaxBudgetUSD != 1e9 {
		t.Fatalf("MaxBudgetUSD = %v", MaxBudgetUSD)
	}
}

// One amount above the guard is refused by every entry point, with the row untouched.
func TestAnAmountAboveTheGuardIsRefused(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := s.Reserve(bg, "default", id, "x", "w", 2e9); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserve: %v", err)
	}
	r := rreserve(t, s, id, "a", 1)
	if err := s.Settle(bg, "default", r.ID, "a", 2e9); !errors.Is(err, ErrInvalid) {
		t.Fatalf("settle: %v", err)
	}
	if rrow(t, s, r.ID).State != ReservationLive {
		t.Fatal("the row is not live")
	}
}

// The edge of the guard keeps the cent: spent just under it plus a settled cent lands exactly on it.
func TestSettleAtTheMarginKeepsTheCent(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 1e9)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, 1e9-0.01, id); err != nil {
		t.Fatal(err)
	}
	r := rreserve(t, s, id, "a", 0.01)
	if err := s.Settle(bg, "default", r.ID, "a", 0.01); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := rspent(t, db, id); got != 1e9 {
		t.Fatalf("spent = %v, want exactly the guard", got)
	}
}

// With no budget a reservation can be a single quantum: the last quantum below the guard lands, the next is refused.
func TestSettleOfTheLastQuantumBelowTheGuard(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, 1e9-0.000001, id); err != nil {
		t.Fatal(err)
	}
	a := rreserve(t, s, id, "a", 0.000001)
	if err := s.Settle(bg, "default", a.ID, "a", 0.000001); err != nil {
		t.Fatalf("the last quantum: %v", err)
	}
	if got := rspent(t, db, id); got != 1e9 {
		t.Fatalf("spent = %v", got)
	}
	b := rreserve(t, s, id, "b", 0.000001)
	if err := s.Settle(bg, "default", b.ID, "b", 0.000001); !errors.Is(err, ErrLimit) {
		t.Fatalf("past the guard: %v", err)
	}
	if rrow(t, s, b.ID).State != ReservationLive || rspent(t, db, id) != 1e9 {
		t.Fatal("a refused settle changed the row or the spend")
	}
}

// A legacy spend above the guard refuses every positive settle (and resolve) and keeps the row.
func TestLegacySpendAboveTheGuardRefusesAPositiveCost(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 0)
	r := rreserve(t, s, id, "a", 1)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 5e11 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 0.5); !errors.Is(err, ErrLimit) {
		t.Fatalf("settle: %v", err)
	}
	if rrow(t, s, r.ID).State != ReservationLive || rspent(t, db, id) != 5e11 {
		t.Fatal("a refused settle changed the row or the spend")
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, _ := s.ExpireStale(bg, "default", id); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if err := s.ResolveReservation(bg, "default", r.ID, 0.5, "x", human); !errors.Is(err, ErrLimit) {
		t.Fatalf("resolve: %v", err)
	}
	if rrow(t, s, r.ID).State != ReservationExpired {
		t.Fatal("the row ended")
	}
	if err := s.ResolveReservation(bg, "default", r.ID, 0, "free", human); err != nil {
		t.Fatalf("a zero-cost resolve: %v", err)
	}
}

// The attack on a scheme that rounds only the sum: at a large spend, many costs of half a float spacing each used
// to grow the spend by a whole spacing. Now each cost is quantized first and charged == spend growth, exactly.
func TestManyTinyCostsGrowTheSpendByExactlyWhatWasCharged(t *testing.T) {
	for name, cost := range map[string]float64{"0.51 quantum": 0.51e-6, "1.49 quantum": 1.49e-6, "1.5 quantum": 1.5e-6, "a cent": 0.01, "0.4 quantum (a zero charge)": 0.4e-6} {
		s, db, _ := newTestStore(t)
		id := rsession(t, s, db, 1e9)
		start := 5e8 + 0.123456
		if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, start, id); err != nil {
			t.Fatal(err)
		}
		var charged int64
		for i := 0; i < 100; i++ {
			r := rreserve(t, s, id, "a", 0.01)
			if err := s.Settle(bg, "default", r.ID, "a", cost); err != nil {
				t.Fatalf("%s #%d: %v", name, i, err)
			}
			got := rrow(t, s, r.ID).ChargedUSD
			if got != float64(micros(cost))/1e6 {
				t.Fatalf("%s: charged %v, want the quantized %v", name, got, float64(micros(cost))/1e6)
			}
			charged += micros(got)
		}
		want := float64(micros(start)+charged) / 1e6
		if got := rspent(t, db, id); got != want {
			t.Fatalf("%s: spent %v, want %v (charged %d micro-dollars)", name, got, want, charged)
		}
	}
}

// A seeded property: whatever the costs, the spend grows by exactly the sum of the quantized costs recorded.
func TestSpendGrowthIsTheSumOfTheQuantizedCosts(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 1e9)
	rng := rand.New(rand.NewSource(42))
	var sum int64
	for i := 0; i < 300; i++ {
		cost := math.Pow(10, -7+rng.Float64()*8) * rng.Float64() // from below a quantum to about 100 dollars
		r := rreserve(t, s, id, "a", 0.01)
		if err := s.Settle(bg, "default", r.ID, "a", cost); err != nil {
			t.Fatalf("#%d cost %v: %v", i, cost, err)
		}
		sum += micros(rrow(t, s, r.ID).ChargedUSD)
		if got := rspent(t, db, id); got != float64(sum)/1e6 {
			t.Fatalf("#%d: spent %v, want %v", i, got, float64(sum)/1e6)
		}
	}
}

// A cost under half a quantum is a zero charge (never a refusal), and the row ends.
func TestACostUnderHalfAQuantumIsAZeroCharge(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 1)
	if err := s.Settle(bg, "default", r.ID, "a", 4e-7); err != nil {
		t.Fatal(err)
	}
	if row := rrow(t, s, r.ID); row.State != ReservationSettled || row.ChargedUSD != 0 || rspent(t, db, id) != 0 {
		t.Fatalf("%+v spent %v", row, rspent(t, db, id))
	}
}

// No budget the store accepts can make a reservation fail.
func TestBudgetsAboveTheGuardAreRefused(t *testing.T) {
	s, _, _ := newTestStore(t)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "big", BudgetUSD: 1.5e9}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create: %v", err)
	}
	se := mustSession(t, s, "default", "ok")
	big := 1.5e9
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

// The grant is computed in whole micro-dollars and never rounds up past what is left: the remainder of an
// off-grid budget is floored, on every branch (a named want and want 0).
func TestReserveNeverGrantsMoreThanIsLeft(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0.0100006)
	r, err := s.Reserve(bg, "default", id, "a", "w", 0.0100007)
	if err != nil {
		t.Fatal(err)
	}
	if r.AmountUSD != 0.01 {
		t.Fatalf("granted %v of a budget of 0.0100006, want 0.01", r.AmountUSD)
	}
	for name, c := range map[string]struct{ budget, want, grant float64 }{
		"all of an off-grid remainder": {0.0100006, 0, 0.01},
		"all of 5.0000009":             {5.0000009, 0, 5},
		"a want rounding up past it":   {5.0000004, 5.0000006, 5},
		"a want below it":              {5.0000009, 2.5, 2.5},
	} {
		id := rsession(t, s, db, c.budget)
		r, err := s.Reserve(bg, "default", id, "a", "w", c.want)
		if err != nil || r.AmountUSD != c.grant {
			t.Errorf("%s: granted %v (%v), want %v", name, r.AmountUSD, err, c.grant)
		}
	}
	id = rsession(t, s, db, 0.0099999)
	if _, err := s.Reserve(bg, "default", id, "a", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("below the minimum grant: %v", err)
	}
}

// For random off-grid budgets and wants, what is granted in total never exceeds the budget.
func TestGrantsNeverExceedAnOffGridBudget(t *testing.T) {
	s, db, _ := newTestStore(t)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 45; i++ { // a profile holds at most 50 open sessions
		budget := math.Round(rng.Float64()*1e9*1e7) / 1e7 // 7 decimals: off the micro-dollar grid
		if i%3 == 0 {
			budget = math.Round(rng.Float64()*50*1e7) / 1e7
		}
		budget = math.Max(budget, 0.01)
		id := rsession(t, s, db, budget)
		var total float64
		for k := 0; k < 8; k++ {
			want := 0.0
			if rng.Intn(3) > 0 {
				want = math.Round(rng.Float64()*budget*1e7) / 1e7
			}
			if k == 0 && i%2 == 0 { // a want within a couple of micro-dollars of the whole budget
				want = math.Round((budget+(rng.Float64()-0.5)*4e-6)*1e7) / 1e7
				want = math.Max(want, 0.0100000)
			}
			r, err := s.Reserve(bg, "default", id, "o", "w", want)
			if err != nil {
				if !errors.Is(err, ErrLimit) {
					t.Fatalf("budget %v want %v: %v", budget, want, err)
				}
				continue
			}
			total += r.AmountUSD
			if total > budget+1e-9+budget*4.5e-16 { // a float64 spacing or two is representation, not money
				t.Fatalf("budget %v: granted %v in total (want %v)", budget, total, want)
			}
		}
	}
}
