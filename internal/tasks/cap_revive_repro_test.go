package tasks

import (
	"testing"
	"time"
)

// A reservation whose lease lapsed past the grace (still 'live', not yet expired) does not count in the accounting
// latch, so a cap run may begin beside it. Heartbeat must not then renew the lapsed row: a live reservation and a live
// cap run would be on the session together (HeartbeatCapRun refuses the mirror case).
func TestHeartbeatOfALapsedReservationBesideALiveCapRun(t *testing.T) {
	s, db, clk := newTestStore(t)
	session := rsession(t, s, db, 10)
	r := rreserve(t, s, session, "o1", 2)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.BeginCapRun(bg, "default", session, "o2", "run"); err != nil {
		t.Fatalf("the reservation is lapsed, so it does not latch: %v", err)
	}
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); err == nil {
		t.Fatalf("the lapsed reservation was renewed beside a live cap run")
	}
}
