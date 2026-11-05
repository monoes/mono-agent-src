package tasks

import (
	"errors"
	"testing"
	"time"
)

// Two handles on one database whose clocks are further apart than the lease and the grace (an NTP step, a resumed
// suspended machine, a VM clock jump). The one-mode rule (a cap run and a reservation are never in flight on a session
// together) must hold whatever either clock says, so a heartbeat is refused by the ORDER of the two rows, not by the
// writer's reading of a lease.
const skewGap = 200 * time.Second // lease 2 min: a row written 200 s behind reads as lapsed by more than the 1 min grace

func skewedHandles(t *testing.T) (behind, ahead *Store, session int64) {
	t.Helper()
	stores := processes(t, 2)
	behind, ahead = stores[0], stores[1]
	cb, ca := &offsetClock{}, &offsetClock{}
	ca.shift.Store(int64(skewGap))
	behind.now, ahead.now = cb.now, ca.now
	session = seedBudget(t, behind.db, 10)
	if _, err := behind.db.Exec(`UPDATE task_sessions SET state = 'running'`); err != nil {
		t.Fatal(err)
	}
	return behind, ahead, session
}

// The handle behind registers a cap run; the handle ahead sees it as lapsed and grants a reservation; the cap run's
// heartbeat (fresh in its own clock) must then be refused, not renewed beside the hold.
func TestSkewCapRunHeartbeatBesideAReservationGrantedByAHandleAheadIsLost(t *testing.T) {
	behind, ahead, session := skewedHandles(t)
	c, err := behind.BeginCapRun(bg, "default", session, "o-run", "run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ahead.ReserveExecAsk(bg, "default", session, "o-res", "w", "e1", 2, 0); err != nil {
		t.Fatalf("the handle ahead did not see the cap run as lapsed (%v): the setup no longer reproduces", err)
	}
	var before string
	if err := behind.db.QueryRow(`SELECT lease_until FROM task_session_cap_runs WHERE id = ?`, c.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := behind.HeartbeatCapRun(bg, "default", c.ID, "o-run"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("cap run renewed beside a reservation: %v", err)
	}
	var after string
	if err := behind.db.QueryRow(`SELECT lease_until FROM task_session_cap_runs WHERE id = ?`, c.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("the refused heartbeat changed the lease: %s -> %s", before, after)
	}
}

// The mirror: the handle behind holds a reservation, the handle ahead sees it as lapsed and registers a cap run; the
// reservation's heartbeat (fresh in its own clock) must be refused.
func TestSkewReservationHeartbeatBesideACapRunRegisteredByAHandleAheadIsLost(t *testing.T) {
	behind, ahead, session := skewedHandles(t)
	r, err := behind.ReserveExecAsk(bg, "default", session, "o-res", "w", "e1", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ahead.BeginCapRun(bg, "default", session, "o-run", "run"); err != nil {
		t.Fatalf("the handle ahead did not see the reservation as lapsed (%v): the setup no longer reproduces", err)
	}
	var before string
	if err := behind.db.QueryRow(`SELECT lease_until FROM task_session_reservations WHERE id = ?`, r.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := behind.Heartbeat(bg, "default", r.ID, "o-res"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("reservation renewed beside a cap run: %v", err)
	}
	var after string
	if err := behind.db.QueryRow(`SELECT lease_until FROM task_session_reservations WHERE id = ?`, r.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("the refused heartbeat changed the lease: %s -> %s", before, after)
	}
}

// A reservation that lapsed before the cap run began, with no other holder, does not block the cap run's own heartbeat
// (the reservation is older than the run, so it is not a hold taken beside it).
func TestCapRunHeartbeatIgnoresAReservationOlderThanItself(t *testing.T) {
	s, db, clk := newTestStore(t)
	session := rsession(t, s, db, 10)
	rreserve(t, s, session, "o1", 2)
	lapse(clk)
	c, err := s.BeginCapRun(bg, "default", session, "o2", "run")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatCapRun(bg, "default", c.ID, "o2"); err != nil {
		t.Fatalf("an older lapsed reservation blocked the cap run: %v", err)
	}
}

// A cap run and a reservation each renew normally when the other kind never was on the session.
func TestHeartbeatsRenewWhenTheOtherKindIsAbsent(t *testing.T) {
	s, db, clk := newTestStore(t)
	capSession := rsession(t, s, db, 10)
	c, err := s.BeginCapRun(bg, "default", capSession, "o1", "run")
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease / 2)
	if err := s.HeartbeatCapRun(bg, "default", c.ID, "o1"); err != nil {
		t.Fatalf("cap run: %v", err)
	}
	resSession := rsession(t, s, db, 10)
	r := rreserve(t, s, resSession, "o2", 2)
	clk.advance(ReservationLease / 2)
	if err := s.Heartbeat(bg, "default", r.ID, "o2"); err != nil {
		t.Fatalf("reservation: %v", err)
	}
}

// The suspended machine, twice: a reservation lapsed past the grace is renewed beside a DEAD cap run, and its next
// renewal is not refused by that run's row; a cap run lapsed past the grace is renewed beside a DEAD reservation, and
// its next renewal is not refused by that reservation.
func TestRenewalOfALapsedRowIsNotRefusedTheSecondTimeByTheDeadRowBesideIt(t *testing.T) {
	s, db, clk := newTestStore(t)
	session := rsession(t, s, db, 10)
	r := rreserve(t, s, session, "o1", 2)
	lapse(clk)
	if _, err := s.BeginCapRun(bg, "default", session, "o2", "run"); err != nil {
		t.Fatal(err)
	}
	lapse(clk)
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	clk.advance(ReservationLease / 2)
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); err != nil {
		t.Fatalf("second: %v", err)
	}

	other := rsession(t, s, db, 10)
	c, err := s.BeginCapRun(bg, "default", other, "o3", "run")
	if err != nil {
		t.Fatal(err)
	}
	lapse(clk)
	lapse(clk)
	// a reservation taken beside the dead run, whose owner then died: it lapses as well
	r2 := rreserve(t, s, other, "o4", 2)
	lapse(clk)
	lapse(clk)
	if r2.ID == 0 {
		t.Fatal("no reservation")
	}
	if err := s.HeartbeatCapRun(bg, "default", c.ID, "o3"); err != nil {
		t.Fatalf("cap run first: %v", err)
	}
	clk.advance(ReservationLease / 2)
	if err := s.HeartbeatCapRun(bg, "default", c.ID, "o3"); err != nil {
		t.Fatalf("cap run second: %v", err)
	}
}
