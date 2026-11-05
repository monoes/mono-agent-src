package tasks

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func lapse(clk *clock) { clk.advance(ReservationLease + ReservationGrace + time.Second) }

// A reservation inside its grace renews beside no cap run, and one lapsed past the grace with no cap run beside it
// still renews (the suspended machine).
func TestHeartbeatRenewsInGraceAndLapsedWithoutACapRun(t *testing.T) {
	s, db, clk := newTestStore(t)
	session := rsession(t, s, db, 10)
	r := rreserve(t, s, session, "o1", 2)
	clk.advance(ReservationLease + ReservationGrace/2)
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); err != nil {
		t.Fatalf("in grace: %v", err)
	}
	lapse(clk)
	lapse(clk)
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); err != nil {
		t.Fatalf("lapsed, no cap run: %v", err)
	}
	if got := rrow(t, s, r.ID); got.State != "live" || got.LeaseUntil < s.now().UTC().Format(timeFmt) {
		t.Fatalf("not renewed: %+v", got)
	}
}

// A lapsed reservation beside a live cap run is refused and its row is not revived.
func TestHeartbeatOfALapsedReservationBesideACapRunIsLostAndNotRevived(t *testing.T) {
	s, db, clk := newTestStore(t)
	session := rsession(t, s, db, 10)
	r := rreserve(t, s, session, "o1", 2)
	lapse(clk)
	if _, err := s.BeginCapRun(bg, "default", session, "o2", "run"); err != nil {
		t.Fatal(err)
	}
	before := rrow(t, s, r.ID)
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("got %v, want ErrReservationLost", err)
	}
	if after := rrow(t, s, r.ID); after.LeaseUntil != before.LeaseUntil || after.State != before.State {
		t.Fatalf("the row changed: %+v -> %+v", before, after)
	}
	// a cap run on another session does not block
	other := rsession(t, s, db, 10)
	if _, err := s.BeginCapRun(bg, "default", other, "o3", "run"); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("still lost: %v", err)
	}
}

// A cap run that itself lapsed past the grace does not block the reservation's renewal.
func TestHeartbeatOfALapsedReservationIgnoresALapsedCapRun(t *testing.T) {
	s, db, clk := newTestStore(t)
	session := rsession(t, s, db, 10)
	r := rreserve(t, s, session, "o1", 2)
	lapse(clk)
	if _, err := s.BeginCapRun(bg, "default", session, "o2", "run"); err != nil {
		t.Fatal(err)
	}
	lapse(clk) // the run's lease lapsed too (both rows are lapsed now)
	if err := s.Heartbeat(bg, "default", r.ID, "o1"); err != nil {
		t.Fatalf("a dead run blocked the renewal: %v", err)
	}
}

// BeginCapRun racing the Heartbeat of a lapsed reservation, in two processes, never ends with both live: one of the
// two is refused. Bounded (5 seeds x 25 rounds).
func TestBeginCapRunRacingAHeartbeatOfALapsedReservationNeverEndsWithBothLive(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint("pass", seed), func(t *testing.T) {
			stores := processes(t, 2)
			db := stores[0].db
			clk := newClock()
			stores[0].now, stores[1].now = clk.now, clk.now
			var beginFirst, beatFirst int
			for i := 0; i < 25; i++ {
				session := seedBudget(t, db, 10)
				r, err := stores[0].Reserve(bg, "default", session, "o1", "w", 2)
				if err != nil {
					t.Fatal(err)
				}
				lapse(clk)
				var beginErr, beatErr error
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					_, beginErr = stores[0].BeginCapRun(bg, "default", session, "o2", "run")
				}()
				go func() { defer wg.Done(); <-start; beatErr = stores[1].Heartbeat(bg, "default", r.ID, "o1") }()
				close(start)
				wg.Wait()
				if beginErr != nil && !errors.Is(beginErr, ErrAccountingMode) {
					t.Fatalf("round %d: begin %v", i, beginErr)
				}
				if beatErr != nil && !errors.Is(beatErr, ErrReservationLost) {
					t.Fatalf("round %d: heartbeat %v", i, beatErr)
				}
				if beginErr == nil && beatErr == nil {
					t.Fatalf("round %d: a live reservation and a live cap run are on the session together", i)
				}
				if beginErr == nil {
					beginFirst++
				} else {
					beatFirst++
				}
			}
			t.Logf("seed %d: run first %d, heartbeat first %d", seed, beginFirst, beatFirst)
		})
	}
}

// A read error of the owner row (here the table is missing under the transaction) is not "the reservation is lost": the
// worker keeps going and the next tick retries.
func TestHeartbeatReadErrorIsNotLost(t *testing.T) {
	s, db, _ := newTestStore(t)
	session := rsession(t, s, db, 10)
	r := rreserve(t, s, session, "o1", 2)
	if _, err := db.Exec(`ALTER TABLE task_session_reservations RENAME TO task_session_reservations_away`); err != nil {
		t.Skipf("cannot make the read fail here: %v", err)
	}
	err := s.Heartbeat(bg, "default", r.ID, "o1")
	if err == nil || errors.Is(err, ErrReservationLost) {
		t.Fatalf("got %v, want a non-lost error", err)
	}
}
