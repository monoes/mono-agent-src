package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// rchain makes descendants under root one below the other, all running; budgets[i] is the i-th one's budget (0: none).
func rchain(t *testing.T, s *Store, db *sql.DB, root int64, budgets ...float64) []int64 {
	t.Helper()
	ids := []int64{root}
	for _, b := range budgets {
		ids = append(ids, rkid(t, s, db, ids[len(ids)-1], b, "running"))
	}
	return ids
}

func TestAnUnbudgetedDescendantIsBoundedByABudgetedAncestorAtEveryDepth(t *testing.T) {
	for depth := 1; depth < HardMaxSessionDepth; depth++ {
		t.Run(fmt.Sprintf("depth %d", depth), func(t *testing.T) {
			s, db, _ := newTestStore(t)
			root := rsession(t, s, db, 10)
			setSpent(t, db, root, 4)
			ids := rchain(t, s, db, root, make([]float64, depth)...)
			leaf := ids[len(ids)-1]
			r, err := s.Reserve(bg, "default", leaf, "a", "w", 0)
			if err != nil {
				t.Fatal(err)
			}
			if !near(r.AmountUSD, 6) || !r.Bounded {
				t.Fatalf("granted %+v, the ancestor had 6 left", r)
			}
			if _, err := s.Reserve(bg, "default", leaf, "b", "w", 0); !errors.Is(err, ErrLimit) {
				t.Fatalf("a second grab past the ancestor's budget: %v", err)
			}
			if c := rcommit(t, s, root); !near(c.Total(), 10) {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestAWantBelowTheAncestorsRemainderWinsAndNoBudgetAboveMeansNoBound(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	ids := rchain(t, s, db, root, 0, 0)
	if r, err := s.Reserve(bg, "default", ids[2], "a", "w", 2.5); err != nil || !near(r.AmountUSD, 2.5) {
		t.Fatalf("%+v, %v", r, err)
	}
	// no budgeted ancestor and no budget: the row records the want and nothing is bounded
	a := rsession(t, s, db, 0)
	kids := rchain(t, s, db, a, 0)
	if r, err := s.Reserve(bg, "default", kids[1], "a", "w", 0); err != nil || r.Bounded || r.AmountUSD != 0 {
		t.Fatalf("%+v, %v", r, err)
	}
	if r, err := s.Reserve(bg, "default", kids[1], "b", "w", 3); err != nil || !r.Bounded || r.AmountUSD != 3 {
		t.Fatalf("%+v, %v", r, err)
	}
}

// Mixed chains: any budgeted level binds, the tightest wins, in whole micro-dollars.
func TestTheTightestBudgetedAncestorBinds(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 100)
	ids := rchain(t, s, db, root, 0, 7.123456, 0) // root(100) > u > mid(7.123456) > leaf
	setSpent(t, db, ids[2], 0.000001)
	r, err := s.Reserve(bg, "default", ids[3], "a", "w", 0)
	if err != nil {
		t.Fatal(err)
	}
	if toMicros(r.AmountUSD) != 7123455 {
		t.Fatalf("granted %d micros", toMicros(r.AmountUSD))
	}
}

// A child with its own open budget has already been counted whole by its parent, so the first part of what it
// reserves costs the ancestor nothing.
func TestAChildWithItsOwnOpenBudgetAbsorbsTheFirstPart(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 5)
	setSpent(t, db, root, 3)
	kid := rkid(t, s, db, root, 2, "running") // the root already counts these 2 as held
	r, err := s.Reserve(bg, "default", kid, "a", "w", 0)
	if err != nil || !near(r.AmountUSD, 2) {
		t.Fatalf("%+v, %v", r, err)
	}
	if c := rcommit(t, s, root); !near(c.Total(), 5) {
		t.Fatalf("%+v", c)
	}
	// an unbudgeted grandchild under a budgeted open child: the child's slack absorbs, then the root binds
	root2 := rsession(t, s, db, 12)
	setSpent(t, db, root2, 3)
	mid := rkid(t, s, db, root2, 7, "running")
	leaf := rkid(t, s, db, mid, 0, "running")
	// root2 has 12-3-7 = 2 free; mid's own budget (7) lets the leaf take 7 before the root notices, but mid binds at 7
	r2, err := s.Reserve(bg, "default", leaf, "a", "w", 0)
	if err != nil || !near(r2.AmountUSD, 7) {
		t.Fatalf("%+v, %v", r2, err)
	}
	if c := rcommit(t, s, root2); !near(c.Total(), 10) {
		t.Fatalf("%+v", c)
	}
}

// The slack of a child's open budget is spent first, then the ancestor's free money: a=4 slack + 1 free = 5.
func TestSlackThenFreeMoneyBoundsAnUnbudgetedGrandchildOfAnUnboundedMiddle(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 12)
	setSpent(t, db, root, 3)
	other := rkid(t, s, db, root, 4, "running") // a sibling that holds its 4 whole
	_ = other
	mid := rkid(t, s, db, root, 0, "running")
	leaf := rkid(t, s, db, mid, 0, "running")
	r, err := s.Reserve(bg, "default", leaf, "a", "w", 0)
	if err != nil || !near(r.AmountUSD, 5) {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestAnAncestorAlreadyOverItsBudgetRefusesEverything(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 5)
	setSpent(t, db, root, 6)
	ids := rchain(t, s, db, root, 0)
	if _, err := s.Reserve(bg, "default", ids[1], "a", "w", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	if rows := countRows(t, db); rows != 0 {
		t.Fatalf("%d rows", rows)
	}
}

func TestAnAncestorGrantBelowTheMinimumIsRefused(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 5)
	setSpent(t, db, root, 4.995)
	ids := rchain(t, s, db, root, 0)
	if _, err := s.Reserve(bg, "default", ids[1], "a", "w", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	setSpent(t, db, root, 4.99)
	if r, err := s.Reserve(bg, "default", ids[1], "a", "w", 1); err != nil || !near(r.AmountUSD, 0.01) {
		t.Fatalf("%+v, %v", r, err)
	}
}

// Own-session rules stay: a budgeted target is also bounded by its own remainder.
func TestTheOwnRulesStillApplyUnderAnAncestor(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 100)
	kid := rkid(t, s, db, root, 3, "running")
	r, err := s.Reserve(bg, "default", kid, "a", "w", 50)
	if err != nil || !near(r.AmountUSD, 3) {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestTheAncestorWalkIsProfileScopedAndFailsClosedOnCyclesAndDepth(t *testing.T) {
	s, db, _ := newTestStore(t)
	addProfile(t, db, "other")
	// a parent that names a session of another profile is not an ancestor
	foreign := rsession(t, s, db, 1)
	if _, err := db.Exec(`UPDATE task_sessions SET profile_id = 'other' WHERE id = ?`, foreign); err != nil {
		t.Fatal(err)
	}
	kid := rkid(t, s, db, foreign, 0, "running")
	if r, err := s.Reserve(bg, "default", kid, "a", "w", 5); err != nil || !near(r.AmountUSD, 5) {
		t.Fatalf("%+v, %v", r, err)
	}
	// a cycle of parents
	a := rsession(t, s, db, 10)
	b := rkid(t, s, db, a, 0, "running")
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(bg, "default", b, "a", "w", 1); err == nil {
		t.Fatal("reserved inside a cycle of parents")
	}
	// a tree one level too deep
	root := rsession(t, s, db, 10)
	ids := rchain(t, s, db, root, 0, 0, 0)
	if _, err := db.Exec(`INSERT INTO task_sessions (profile_id, title, parent_session_id, state, created_by, created_at, updated_at) VALUES ('default','deep',?, 'running','you',?,?)`, ids[len(ids)-1], rowTime, rowTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(bg, "default", ids[len(ids)-1], "a", "w", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_session_reservations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ReservationHeld and LiveReservations.

func TestReservationHeldSumsLiveAndExpiredRowsInMicroDollars(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 0)
	other := rsession(t, s, db, 0)
	a := rreserve(t, s, sid, "a", 3.000001)
	b := rreserve(t, s, sid, "b", 2)
	c := rreserve(t, s, sid, "c", 4)
	d := rreserve(t, s, sid, "d", 1)
	rreserve(t, s, other, "z", 9)
	if err := s.ChargeReservation(bg, "default", a.ID, "a", 1.5); err != nil {
		t.Fatal(err)
	}
	if err := s.ChargeReservation(bg, "default", b.ID, "b", 2.5); err != nil { // overcharged: holds 0, never negative
		t.Fatal(err)
	}
	if err := s.Settle(bg, "default", c.ID, "c", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReservation(bg, "default", d.ID, "d"); err != nil {
		t.Fatal(err)
	}
	got, err := ReservationHeld(bg, db, "default", sid)
	if err != nil || got != 1500001 {
		t.Fatalf("%d, %v", got, err)
	}
	// an expired row keeps holding
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.ExpireStale(bg, "default", sid); err != nil {
		t.Fatal(err)
	}
	if got, err := ReservationHeld(bg, db, "default", sid); err != nil || got != 1500001 {
		t.Fatalf("%d, %v", got, err)
	}
	// it works on a transaction too, and a wrong profile sees nothing
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if got, err := ReservationHeld(bg, tx, "default", sid); err != nil || got != 1500001 {
		t.Fatalf("%d, %v", got, err)
	}
	addProfile(t, db, "other")
	if got, err := ReservationHeld(bg, db, "other", sid); err != nil || got != 0 {
		t.Fatalf("%d, %v", got, err)
	}
}

func TestLiveReservationsListsOnlyTheProfilesRowsNotLapsedPastGrace(t *testing.T) {
	s, db, clk := newTestStore(t)
	addProfile(t, db, "other")
	sid := rsession(t, s, db, 0)
	old := rreserve(t, s, sid, "old", 1)
	gone := rreserve(t, s, sid, "gone", 1)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	fresh := rreserve(t, s, sid, "fresh", 2)
	if err := s.ChargeReservation(bg, "default", fresh.ID, "fresh", 0.5); err != nil {
		t.Fatal(err)
	}
	// "old" lapsed past grace but is unswept: not live any more; "gone" is swept
	if n, err := s.ExpireStale(bg, "default", sid); err != nil || n != 2 {
		t.Fatalf("%d, %v", n, err)
	}
	_, _ = old, gone
	rows, err := s.LiveReservations(bg, db, "default")
	if err != nil || len(rows) != 1 || rows[0].ID != fresh.ID || rows[0].SessionID != sid || rows[0].WorkerRef != "worker-fresh" || rows[0].State != ReservationLive || rows[0].AmountUSD != 2 || rows[0].ChargedUSD != 0.5 {
		t.Fatalf("%+v, %v", rows, err)
	}
	if rows, err := s.LiveReservations(bg, db, "other"); err != nil || len(rows) != 0 {
		t.Fatalf("%+v, %v", rows, err)
	}
}

func TestLiveReservationsExcludesALapsedUnsweptRowOnlyPastTheGrace(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 0)
	r := rreserve(t, s, sid, "a", 1)
	clk.advance(ReservationLease + ReservationGrace/2)
	if rows, err := s.LiveReservations(bg, db, "default"); err != nil || len(rows) != 1 {
		t.Fatalf("%+v, %v", rows, err)
	}
	clk.advance(ReservationGrace)
	if rows, err := s.LiveReservations(bg, db, "default"); err != nil || len(rows) != 0 {
		t.Fatalf("%+v, %v", rows, err)
	}
	_ = r
}

// Races: separate handles to one file fight for the ancestor's last dollar.
func TestConcurrentUnbudgetedDescendantsNeverTakeMoreThanTheAncestorHas(t *testing.T) {
	const k, each = 8, 4
	stores := processes(t, k)
	root := seedBudget(t, stores[0].db, 10)
	setSpent(t, stores[0].db, root, 0.25)
	mid := rkid(t, stores[0], stores[0].db, root, 0, "running")
	leaves := []int64{
		rkid(t, stores[0], stores[0].db, mid, 0, "running"),
		rkid(t, stores[0], stores[0].db, mid, 0, "running"),
		rkid(t, stores[0], stores[0].db, root, 0, "running"),
	}
	var mu sync.Mutex
	granted := 0.0
	var wg sync.WaitGroup
	errs := make(chan error, k*each)
	for p := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := fmt.Sprintf("proc-%d", p)
			for i := 0; i < each; i++ {
				want := 0.0
				if i%2 == 0 {
					want = 1.7
				}
				r, err := stores[p].Reserve(bg, "default", leaves[(p+i)%len(leaves)], owner, fmt.Sprintf("w%d", i), want)
				switch {
				case err == nil:
					mu.Lock()
					granted += r.AmountUSD
					mu.Unlock()
				case errors.Is(err, ErrLimit):
				default:
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("reserve: %v", err)
	}
	if granted > 10-0.25+1e-9 {
		t.Fatalf("granted %v where the ancestor had %v", granted, 10-0.25)
	}
	if granted < 10-0.25-0.01 {
		t.Fatalf("granted only %v of %v although the first caller to want 0 takes the rest", granted, 10-0.25)
	}
	if c := rcommit(t, stores[0], root); c.Total() > 10+1e-9 {
		t.Fatalf("%+v", c)
	}
}
