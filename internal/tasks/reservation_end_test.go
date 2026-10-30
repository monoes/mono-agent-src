package tasks

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatRenewsOnlyTheOwnersLiveRow(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	clk.advance(time.Minute)
	if err := s.Heartbeat(bg, "default", r.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if got, want := rrow(t, s, r.ID).LeaseUntil, clk.t.Add(ReservationLease).Format(timeFmt); got != want {
		t.Fatalf("lease %s, want %s", got, want)
	}
	before := rrow(t, s, r.ID)
	clk.advance(time.Second)
	for name, err := range map[string]error{
		"another owner":   s.Heartbeat(bg, "default", r.ID, "b"),
		"empty owner":     s.Heartbeat(bg, "default", r.ID, ""),
		"another profile": s.Heartbeat(bg, "nobody", r.ID, "a"),
		"unknown id":      s.Heartbeat(bg, "default", r.ID+99, "a"),
	} {
		if !errors.Is(err, ErrReservationLost) && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if after := rrow(t, s, r.ID); after != before {
		t.Fatalf("a stranger's heartbeat changed the row: %+v -> %+v", before, after)
	}
}

// A lapsed row nobody has expired is still renewed; once expired, the owner's heartbeat finds nothing and the worker stops.
func TestHeartbeatSurvivesSuspendUntilSweptThenFails(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	clk.advance(ReservationLease + 30*time.Second) // lapsed, inside the grace
	if n, err := s.ExpireStale(bg, "default", id); err != nil || n != 0 {
		t.Fatalf("inside the grace: %d, %v", n, err)
	}
	if err := s.Heartbeat(bg, "default", r.ID, "a"); err != nil {
		t.Fatalf("a lapsed, unswept row: %v", err)
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, err := s.ExpireStale(bg, "default", id); err != nil || n != 1 {
		t.Fatalf("past the grace: %d, %v", n, err)
	}
	if err := s.Heartbeat(bg, "default", r.ID, "a"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("after the sweep: %v", err)
	}
}

func TestLeaseNeverMovesBack(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	was := rrow(t, s, r.ID).LeaseUntil
	clk.advance(-time.Hour) // a clock that stepped back
	if err := s.Heartbeat(bg, "default", r.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if got := rrow(t, s, r.ID).LeaseUntil; got != was {
		t.Fatalf("lease went from %s to %s", was, got)
	}
}

func TestSettleChargesTheSpendAndEndsTheRowInOneStep(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	if err := s.Settle(bg, "default", r.ID, "a", 1.25); err != nil {
		t.Fatal(err)
	}
	got := rrow(t, s, r.ID)
	if got.State != ReservationSettled || !near(got.ChargedUSD, 1.25) || got.EndedAt == "" || got.Held() != 0 {
		t.Fatalf("row = %+v", got)
	}
	if !near(rspent(t, db, id), 1.25) {
		t.Fatalf("spent = %v", rspent(t, db, id))
	}
	c := rcommit(t, s, id)
	if !near(c.Total(), 1.25) {
		t.Fatalf("commitment = %+v: the unspent part is free again", c)
	}
	if n := countWhere(t, db, "task_session_events", "session_id = ? AND kind = 'spend'", id); n != 1 {
		t.Fatalf("%d spend event(s)", n)
	}
}

func TestSettleRecordsOverspendAsItHappened(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	if err := s.Settle(bg, "default", r.ID, "a", 6); err != nil {
		t.Fatal(err)
	}
	if !near(rspent(t, db, id), 6) {
		t.Fatalf("spent = %v", rspent(t, db, id))
	}
}

// Owner fencing: a stranger, a second settle and a settle after expiry change nothing and cost nothing.
func TestSettleIsFencedOnOwnerAndLiveState(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	before := rrow(t, s, r.ID)
	for _, owner := range []string{"b", "", "A"} {
		if err := s.Settle(bg, "default", r.ID, owner, 3); !errors.Is(err, ErrReservationLost) {
			t.Fatalf("owner %q: %v", owner, err)
		}
	}
	if err := s.Settle(bg, "nobody", r.ID, "a", 3); err == nil {
		t.Fatal("another profile settled")
	}
	if after := rrow(t, s, r.ID); after != before || rspent(t, db, id) != 0 {
		t.Fatalf("a stranger's settle changed things: %+v spent %v", after, rspent(t, db, id))
	}
	if err := s.Settle(bg, "default", r.ID, "a", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 2); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("second settle: %v", err)
	}
	if !near(rspent(t, db, id), 2) {
		t.Fatalf("spent twice: %v", rspent(t, db, id))
	}
	// after expiry the owner's settle is refused too: that cost is recorded only through the operator's resolve
	r2 := rreserve(t, s, id, "a", 3)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, _ := s.ExpireStale(bg, "default", id); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if err := s.Settle(bg, "default", r2.ID, "a", 1); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("settle after expiry: %v", err)
	}
	if !near(rspent(t, db, id), 2) {
		t.Fatalf("spent = %v", rspent(t, db, id))
	}
}

func TestSettleRefusesBadCosts(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	for _, v := range []float64{math.NaN(), math.Inf(1), -1, 1e13} {
		if err := s.Settle(bg, "default", r.ID, "a", v); !errors.Is(err, ErrInvalid) {
			t.Errorf("cost %v: %v", v, err)
		}
	}
	if rrow(t, s, r.ID).State != ReservationLive || rspent(t, db, id) != 0 {
		t.Fatal("a refused settle changed the row")
	}
}

func TestReleaseGivesTheMoneyBackAndIsFenced(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "a", 4)
	if err := s.ReleaseReservation(bg, "default", r.ID, "b"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("stranger: %v", err)
	}
	if !near(rcommit(t, s, id).Held, 4) {
		t.Fatal("a stranger's release freed the money")
	}
	if err := s.ReleaseReservation(bg, "default", r.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if c := rcommit(t, s, id); c.Held != 0 || rspent(t, db, id) != 0 {
		t.Fatalf("%+v", c)
	}
	if err := s.ReleaseReservation(bg, "default", r.ID, "a"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("second release: %v", err)
	}
}

// Crash model: a handle dropped without settling is expired by any process and STILL holds its money (I4).
func TestACrashedHolderIsExpiredAndStillHolds(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "dead", 6)
	before := rcommit(t, s, id)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, err := s.ExpireStale(bg, "default", 0); err != nil || n != 1 { // any process, the whole profile
		t.Fatalf("%d, %v", n, err)
	}
	got := rrow(t, s, r.ID)
	if got.State != ReservationExpired || got.EndedAt == "" {
		t.Fatalf("row = %+v", got)
	}
	if after := rcommit(t, s, id); after != before || !near(after.Held, 6) {
		t.Fatalf("held went from %+v to %+v", before, after)
	}
	if _, err := s.Reserve(bg, "default", id, "new", "w", 5); err != nil {
		t.Fatalf("the free 4 stays reservable: %v", err)
	}
	if _, err := s.Reserve(bg, "default", id, "new2", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("the expired 6 is not freed: %v", err)
	}
	if n, _ := s.ExpireStale(bg, "default", 0); n != 0 {
		t.Fatalf("expired again: %d", n)
	}
	if n := countWhere(t, db, "task_session_events", "session_id = ? AND kind = 'reservation_expired'", id); n != 1 {
		t.Fatalf("%d expiry event(s)", n)
	}
}

func TestExpireStaleTouchesOnlyLapsedRowsOfItsScope(t *testing.T) {
	s, db, clk := newTestStore(t)
	addProfile(t, db, "other")
	a, b := rsession(t, s, db, 10), rsession(t, s, db, 10)
	ra, rb := rreserve(t, s, a, "o", 1), rreserve(t, s, b, "o", 1)
	foreign := mustSession(t, s, "other", "f")
	rf, err := s.Reserve(bg, "other", foreign.ID, "o", "w", 1)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	fresh := rreserve(t, s, a, "fresh", 1)
	if n, err := s.ExpireStale(bg, "default", a); err != nil || n != 1 {
		t.Fatalf("one session: %d, %v", n, err)
	}
	if rrow(t, s, ra.ID).State != ReservationExpired || rrow(t, s, rb.ID).State != ReservationLive || rrow(t, s, fresh.ID).State != ReservationLive {
		t.Fatal("wrong rows expired")
	}
	var st string
	if err := db.QueryRow(`SELECT state FROM task_session_reservations WHERE id = ?`, rf.ID).Scan(&st); err != nil || st != "live" {
		t.Fatalf("another profile's row: %q %v", st, err)
	}
	if _, err := s.ExpireStale(bg, "ghost", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestResolveIsTheOperatorsAndOnlyForAnExpiredRow(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	live := rreserve(t, s, id, "a", 4)
	if err := s.ResolveReservation(bg, "default", live.ID, 1, "n", human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a live row: %v", err)
	}
	dead := rreserve(t, s, id, "dead", 5)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.ExpireStale(bg, "default", id); err != nil {
		t.Fatal(err)
	}
	for _, who := range []Actor{bot("w"), {Kind: Capture}, {Kind: Agent, Name: "a", Session: id}} {
		if err := s.ResolveReservation(bg, "default", dead.ID, 0, "n", who); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%+v: %v", who, err)
		}
	}
	if rrow(t, s, dead.ID).State != ReservationExpired {
		t.Fatal("a non-operator resolved it")
	}
	if err := s.ResolveReservation(bg, "default", dead.ID, math.NaN(), "n", human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NaN: %v", err)
	}
	if err := s.ResolveReservation(bg, "default", dead.ID, 2, "worker finished\x00 but never reported", human); err != nil {
		t.Fatal(err)
	}
	got := rrow(t, s, dead.ID)
	if got.State != ReservationResolved || !near(got.ChargedUSD, 2) || strings.ContainsRune(got.EndNote, 0) {
		t.Fatalf("row = %+v", got)
	}
	if !near(rspent(t, db, id), 2) || !near(rcommit(t, s, id).Held, 4) {
		t.Fatalf("spent %v commitment %+v: the 3 not charged is free", rspent(t, db, id), rcommit(t, s, id))
	}
	if err := s.ResolveReservation(bg, "default", dead.ID, 2, "n", human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("second resolve: %v", err)
	}
	if !near(rspent(t, db, id), 2) {
		t.Fatal("charged twice")
	}
}

func TestResolveWithZeroFreesTheWholeReservation(t *testing.T) {
	s, db, clk := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := rreserve(t, s, id, "dead", 10)
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.ExpireStale(bg, "default", id); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveReservation(bg, "default", r.ID, 0, "", human); err != nil {
		t.Fatal(err)
	}
	if c := rcommit(t, s, id); c.Total() != 0 || rspent(t, db, id) != 0 {
		t.Fatalf("%+v", c)
	}
}

// Decision for session and profile deletes: the rows go with them, the money returns with no counter to repair,
// and the worker's next heartbeat finds nothing and stops.
func TestDeletingTheSessionFreesItsReservations(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 100)
	kid := rkid(t, s, db, root, 0, "running")
	r := rreserve(t, s, kid, "a", 30)
	if !near(rcommit(t, s, root).Children, 30) {
		t.Fatal("the child's hold is not counted")
	}
	if _, err := db.Exec(`DELETE FROM `+"task_sessions"+` WHERE id = ?`, kid); err != nil {
		t.Fatal(err)
	}
	if c := rcommit(t, s, root); c.Total() != 0 {
		t.Fatalf("%+v", c)
	}
	if err := s.Heartbeat(bg, "default", r.ID, "a"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("heartbeat after the delete: %v", err)
	}
	if err := s.Settle(bg, "default", r.ID, "a", 1); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("settle after the delete: %v", err)
	}
}
