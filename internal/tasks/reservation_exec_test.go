package tasks

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// Tests of the per-exec reservation API (migration 154): ReserveExec is idempotent per (session, worker, exec),
// ChargeExec charges MAX(charged so far, the largest cumulative THIS exec reported), and a run that reports
// nothing is settled at its whole grant.

func xreserve(t *testing.T, s *Store, session int64, owner, worker, exec string, want float64) Reservation {
	t.Helper()
	r, err := s.ReserveExec(bg, "default", session, owner, worker, exec, want)
	if err != nil {
		t.Fatalf("reserve exec %s/%s for %v: %v", worker, exec, want, err)
	}
	return r
}

func xcount(t *testing.T, db *sql.DB, session int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_session_reservations WHERE session_id = ?`, session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReserveExecIsIdempotentPerExec(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	a := xreserve(t, s, id, "o1", "w", "e1", 4)
	if a.ExecID != "e1" || !near(a.AmountUSD, 4) || a.State != ReservationLive {
		t.Fatalf("first = %+v", a)
	}
	rev := func() int64 {
		var v int64
		if err := db.QueryRow(`SELECT COALESCE((SELECT rev FROM task_session_rev WHERE session_id = ?), 0)`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := rev()
	// a retry (even asking for another amount) is the same row and takes no second hold
	b := xreserve(t, s, id, "o1", "w", "e1", 9)
	if b.ID != a.ID || !near(b.AmountUSD, 4) {
		t.Fatalf("retry = %+v, want row #%d at $4", b, a.ID)
	}
	if n := xcount(t, db, id); n != 1 {
		t.Fatalf("%d rows", n)
	}
	if c := rcommit(t, s, id); !near(c.Held, 4) {
		t.Fatalf("held %v, want 4: %+v", c.Held, c)
	}
	if rev() != before {
		t.Fatal("an idempotent hit must not move the session revision")
	}
	// another exec of the same worker is a new hold; another worker with the same exec id too
	c := xreserve(t, s, id, "o1", "w", "e2", 3)
	d := xreserve(t, s, id, "o1", "w2", "e1", 2)
	if c.ID == a.ID || d.ID == a.ID || c.ID == d.ID || xcount(t, db, id) != 3 {
		t.Fatalf("rows %d %d %d", a.ID, c.ID, d.ID)
	}
	// a different owner cannot adopt the exec
	if _, err := s.ReserveExec(bg, "default", id, "o2", "w", "e1", 4); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("other owner: %v", err)
	}
	// after the exec ended the retry returns the ended row and holds nothing
	if err := s.ChargeExec(bg, "default", a.ID, "o1", 1, true); err != nil {
		t.Fatal(err)
	}
	e := xreserve(t, s, id, "o1", "w", "e1", 4)
	if e.ID != a.ID || e.State != ReservationSettled || xcount(t, db, id) != 3 {
		t.Fatalf("retry after settle = %+v", e)
	}
}

func TestReserveExecNeedsAnExecID(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	for _, bad := range []string{"", "a\x00b", strings.Repeat("x", 257)} {
		if _, err := s.ReserveExec(bg, "default", id, "o", "w", bad, 1); !errors.Is(err, ErrInvalid) {
			t.Errorf("exec id %q: %v", bad, err)
		}
	}
	if xcount(t, db, id) != 0 {
		t.Fatal("a refused call left a row")
	}
}

func TestChargeExecChargesTheMaxOfWhatThisExecReported(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "o", "w", "e1", 6)
	for _, step := range []struct{ report, charged float64 }{{1, 1}, {3, 3}, {2, 3}, {3, 3}, {4.5, 4.5}} {
		if err := s.ChargeExec(bg, "default", r.ID, "o", step.report, false); err != nil {
			t.Fatalf("report %v: %v", step.report, err)
		}
		if got := rrow(t, s, r.ID).ChargedUSD; !near(got, step.charged) || !near(rspent(t, db, id), step.charged) {
			t.Fatalf("after reporting %v: charged %v spent %v, want %v", step.report, got, rspent(t, db, id), step.charged)
		}
	}
	// a LOWER final report never lowers the charge
	if err := s.ChargeExec(bg, "default", r.ID, "o", 2, true); err != nil {
		t.Fatal(err)
	}
	row := rrow(t, s, r.ID)
	if row.State != ReservationSettled || !near(row.ChargedUSD, 4.5) || !near(rspent(t, db, id), 4.5) || row.EndedAt == "" {
		t.Fatalf("%+v spent %v", row, rspent(t, db, id))
	}
	if c := rcommit(t, s, id); !near(c.Held, 0) || !near(c.Spent, 4.5) {
		t.Fatalf("%+v", c)
	}
}

func TestChargeExecFinalAboveTheGrantIsRecordedAsOverspend(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "o", "w", "e1", 2)
	if err := s.ChargeExec(bg, "default", r.ID, "o", 2.75, true); err != nil {
		t.Fatal(err)
	}
	if !near(rspent(t, db, id), 2.75) || !near(rrow(t, s, r.ID).ChargedUSD, 2.75) {
		t.Fatalf("spent %v", rspent(t, db, id))
	}
}

func TestChargeExecWithoutAReportIsSettledAtTheWholeGrant(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 100)
	r := xreserve(t, s, id, "o", "w", "e1", 4)
	if err := s.SettleExecNoReport(bg, "default", r.ID, "o"); err != nil {
		t.Fatal(err)
	}
	row := rrow(t, s, r.ID)
	if row.State != ReservationSettled || !near(row.ChargedUSD, 4) || !near(rspent(t, db, id), 4) {
		t.Fatalf("%+v spent %v: a run that reported nothing costs its whole grant", row, rspent(t, db, id))
	}
	// a run that reported only PARTIAL costs and then ended without a final report is also charged its whole grant
	r2 := xreserve(t, s, id, "o", "w", "e2", 3)
	if err := s.ChargeExec(bg, "default", r2.ID, "o", 1.25, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleExecNoReport(bg, "default", r2.ID, "o"); err != nil {
		t.Fatal(err)
	}
	if row := rrow(t, s, r2.ID); !near(row.ChargedUSD, 3) || row.State != ReservationSettled || !near(rspent(t, db, id), 4+3) {
		t.Fatalf("%+v spent %v, want the whole grant 3 (spent 7 in all)", row, rspent(t, db, id))
	}
	// partial reports above the grant (an overspend) keep what they said: never below what was reported
	r4 := xreserve(t, s, id, "o", "w", "e4", 1)
	if err := s.ChargeExec(bg, "default", r4.ID, "o", 2.5, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleExecNoReport(bg, "default", r4.ID, "o"); err != nil {
		t.Fatal(err)
	}
	if row := rrow(t, s, r4.ID); !near(row.ChargedUSD, 2.5) {
		t.Fatalf("%+v", row)
	}
	// the ended row cannot be settled again
	if err := s.SettleExecNoReport(bg, "default", r.ID, "o"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("second settle: %v", err)
	}
	// and a stranger cannot settle it
	r5 := xreserve(t, s, id, "o", "w", "e5", 1)
	if err := s.SettleExecNoReport(bg, "default", r5.ID, "stranger"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("stranger: %v", err)
	}
	// a run that reported a zero cost DID report: it is free
	r3 := xreserve(t, s, id, "o", "w", "e3", 3)
	if err := s.ChargeExec(bg, "default", r3.ID, "o", 0, true); err != nil {
		t.Fatal(err)
	}
	if row := rrow(t, s, r3.ID); !near(row.ChargedUSD, 0) || row.State != ReservationSettled {
		t.Fatalf("%+v", row)
	}
}

func TestChargeExecRefusesWhatIsNotAMoneyReport(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "o", "w", "e1", 4)
	// a miscomputed negative (-1 included) is an error, never "the whole grant"
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.5, -1, MaxBudgetUSD * 2} {
		for _, final := range []bool{true, false} {
			if err := s.ChargeExec(bg, "default", r.ID, "o", bad, final); !errors.Is(err, ErrInvalid) {
				t.Errorf("%v final=%v: %v", bad, final, err)
			}
		}
	}
	if row := rrow(t, s, r.ID); row.State != ReservationLive || row.ChargedUSD != 0 {
		t.Fatalf("%+v", row)
	}
}

func TestChargeExecIsFencedToTheOwnerAndTheLiveRow(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "o", "w", "e1", 4)
	if err := s.ChargeExec(bg, "default", r.ID, "stranger", 1, false); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("stranger: %v", err)
	}
	if err := s.ChargeExec(bg, "other", r.ID, "o", 1, false); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("other profile: %v", err)
	}
	if err := s.ChargeExec(bg, "default", r.ID+99, "o", 1, false); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("no such row: %v", err)
	}
	if rspent(t, db, id) != 0 || rrow(t, s, r.ID).ChargedUSD != 0 {
		t.Fatal("a refused call charged")
	}
	if err := s.ChargeExec(bg, "default", r.ID, "o", 1, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ChargeExec(bg, "default", r.ID, "o", 3, false); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("after the final: %v", err)
	}
	if !near(rspent(t, db, id), 1) {
		t.Fatalf("spent %v", rspent(t, db, id))
	}
}

// The exec fence: what exec 1 of a worker reports or does late never reaches exec 2 of the same worker.
func TestStaleExecCannotTouchTheNextExecOfTheSameWorker(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	e1 := xreserve(t, s, id, "o", "w", "e1", 4)
	if err := s.ChargeExec(bg, "default", e1.ID, "o", 3, false); err != nil {
		t.Fatal(err)
	}
	e2 := xreserve(t, s, id, "o", "w", "e2", 4)
	if e2.ID == e1.ID {
		t.Fatal("exec 2 got exec 1's row")
	}
	// exec 2's max is its own: exec 1's 3 does not make a lower final of exec 2 cost 3
	if err := s.ChargeExec(bg, "default", e2.ID, "o", 1, true); err != nil {
		t.Fatal(err)
	}
	if row := rrow(t, s, e2.ID); !near(row.ChargedUSD, 1) {
		t.Fatalf("exec 2 charged %v, want 1", row.ChargedUSD)
	}
	// exec 1 ends; its late calls change nothing of exec 3, which is live and untouched
	if err := s.ChargeExec(bg, "default", e1.ID, "o", 3.5, true); err != nil {
		t.Fatal(err)
	}
	e3 := xreserve(t, s, id, "o", "w", "e3", 2)
	for _, late := range []func() error{
		func() error { return s.ChargeExec(bg, "default", e1.ID, "o", 9, false) },
		func() error { return s.ChargeExec(bg, "default", e2.ID, "o", 9, false) },
		func() error { return s.Heartbeat(bg, "default", e1.ID, "o") },
		func() error { return s.Heartbeat(bg, "default", e2.ID, "o") },
	} {
		if err := late(); !errors.Is(err, ErrReservationLost) {
			t.Fatalf("late call: %v", err)
		}
	}
	if row := rrow(t, s, e3.ID); row.ChargedUSD != 0 || row.State != ReservationLive || row.LeaseUntil != e3.LeaseUntil {
		t.Fatalf("exec 3 moved: %+v", row)
	}
	if !near(rspent(t, db, id), 3.5+1) {
		t.Fatalf("spent %v, want 4.5", rspent(t, db, id))
	}
}

// Crash between reserve and settle: nothing is ever settled, the lease lapses, the sweep expires the row, and it
// still holds amount - charged until the operator resolves it.
func TestCrashBetweenReserveAndSettleKeepsTheMoneyHeld(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "dies", "w", "e1", 7)
	if err := s.ChargeExec(bg, "default", r.ID, "dies", 2, false); err != nil {
		t.Fatal(err)
	}
	// the process is gone: no heartbeat, no settle. Before the grace is over nothing is expired.
	clk.advance(ReservationLease + ReservationGrace - time.Second)
	if n, err := s.ExpireStale(bg, "default", id); err != nil || n != 0 {
		t.Fatalf("%d, %v", n, err)
	}
	clk.advance(2 * time.Second)
	if n, err := s.ExpireStale(bg, "default", id); err != nil || n != 1 {
		t.Fatalf("%d, %v", n, err)
	}
	if row := rrow(t, s, r.ID); row.State != ReservationExpired || !near(row.ChargedUSD, 2) {
		t.Fatalf("%+v", row)
	}
	c := rcommit(t, s, id)
	if left, _ := c.Left(); !near(left, 3) {
		t.Fatalf("left %v, want 3 (budget 10 - spent 2 - still held 5): %+v", left, c)
	}
	// the held dollars are not for sale: the 3 left can be reserved, then a further dollar is refused
	last := xreserve(t, s, id, "o2", "w2", "e1", 0)
	if !near(last.AmountUSD, 3) {
		t.Fatalf("%+v", last)
	}
	if _, err := s.ReserveExec(bg, "default", id, "o3", "w3", "e1", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("a new reservation for the held dollar: %v, want ErrLimit", err)
	}
	// the dead exec cannot come back to life
	if err := s.ChargeExec(bg, "default", r.ID, "dies", 7, true); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("charge of an expired row: %v", err)
	}
	// until the operator resolves it: then its held part is free again
	if err := s.ResolveReservation(bg, "default", r.ID, 2, "crashed", Actor{Kind: Human, Name: "op"}); err != nil {
		t.Fatal(err)
	}
	if got := xreserve(t, s, id, "o4", "w4", "e1", 1); !near(got.AmountUSD, 1) {
		t.Fatalf("after the resolve: %+v", got)
	}
}

func TestExecReservationsAreFencedByAncestorBudgets(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 5)
	kid := rkid(t, s, db, root, 0, "running")
	r := xreserve(t, s, kid, "o", "w", "e1", 100)
	if !near(r.AmountUSD, 5) || !r.Bounded {
		t.Fatalf("a worker without budget under a budgeted ancestor is capped at the ancestor's remainder: %+v", r)
	}
	if _, err := s.ReserveExec(bg, "default", kid, "o", "w2", "e1", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
}

// The delete fence still sees an exec reservation, and the cost record can be neither edited nor removed by hand.
func TestExecReservationKeepsTheDeleteFence(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "o", "w", "e1", 4)
	if err := s.ChargeExec(bg, "default", r.ID, "o", 1, false); err != nil {
		t.Fatal(err)
	}
	live, err := s.LiveReservations(bg, db, "default")
	if err != nil || len(live) != 1 || live[0].ID != r.ID || live[0].ExecID != "e1" {
		t.Fatalf("%+v, %v", live, err)
	}
	for _, stmt := range []string{
		`DELETE FROM task_session_reservations WHERE id = ?`,
		`DELETE FROM task_session_reservation_costs WHERE reservation_id = ?`,
		`UPDATE task_session_reservation_costs SET cumulative_usd = 0 WHERE reservation_id = ?`,
		`UPDATE task_session_reservations SET exec_id = 'x' WHERE id = ?`,
	} {
		if _, err := db.Exec(stmt, r.ID); err == nil {
			t.Errorf("allowed by hand: %s", stmt)
		}
	}
	// the session's removal is the way back: the rows and their cost record go with it
	if _, err := db.Exec(`DELETE FROM task_sessions WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_session_reservation_costs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d cost rows left, %v", n, err)
	}
	if err := s.ChargeExec(bg, "default", r.ID, "o", 2, false); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("%v", err)
	}
}

// The table, not the Go code, keeps one exec to one hold.
func TestTheTableRefusesASecondHoldForOneExec(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	xreserve(t, s, id, "o", "w", "e1", 1)
	ins := `INSERT INTO task_session_reservations (profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at, exec_id)
		VALUES ('default', ?, 'w', 'o', 1, 1, 'live', ?, ?, ?, ?)`
	if _, err := db.Exec(ins, id, rowTime, rowTime, rowTime, "e1"); err == nil {
		t.Fatal("two holds for one exec")
	}
	if _, err := db.Exec(ins, id, rowTime, rowTime, rowTime, "e9"); err != nil {
		t.Fatalf("another exec: %v", err)
	}
	// legacy rows (no exec id) are not constrained
	leg := `INSERT INTO task_session_reservations (profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at)
		VALUES ('default', ?, 'w', 'o', 1, 1, 'live', ?, ?, ?)`
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(leg, id, rowTime, rowTime, rowTime); err != nil {
			t.Fatalf("legacy row %d: %v", i, err)
		}
	}
}
