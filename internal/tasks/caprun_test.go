package tasks

import (
	"errors"
	"testing"
	"time"
)

func rexec(t *testing.T, s *Store, session int64, owner, exec string, want float64) (Reservation, error) {
	t.Helper()
	return s.ReserveExec(bg, "default", session, owner, "w-"+owner, exec, want)
}

func capRows(t *testing.T, s *Store, session int64) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_session_cap_runs WHERE session_id = ?`, session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A registered cap run keeps every reservation off the session: its dollars are not held, so a reservation could take
// the same ones. Both reserve calls are refused, nothing is held, and no row is written.
func TestReservationRefusedWhileACapRunIsInFlight(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 5)
	if _, err := s.BeginCapRun(bg, "default", sid, "o1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := rexec(t, s, sid, "o2", "e1", 3); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("ReserveExec: %v", err)
	}
	if _, err := s.Reserve(bg, "default", sid, "o2", "w", 3); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("Reserve: %v", err)
	}
	if c := rcommit(t, s, sid); c.Held != 0 {
		t.Fatalf("held %v", c.Held)
	}
}

// And the other way: a live reservation keeps cap runs out until it has ended.
func TestCapRunRefusedWhileAReservationIsLive(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 5)
	r, err := rexec(t, s, sid, "o1", "e1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCapRun(bg, "default", sid, "o2", "run-1"); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("%v", err)
	}
	if capRows(t, s, sid) != 0 {
		t.Fatal("a refused registration left a row")
	}
	if err := s.ChargeExec(bg, "default", r.ID, "o1", 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCapRun(bg, "default", sid, "o2", "run-1"); err != nil {
		t.Fatalf("drained: %v", err)
	}
}

// The drain: a cap run's end lets reservations in; two cap runs both have to end.
func TestModeFlipsOnlyAfterTheDrain(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 5)
	a, _ := s.BeginCapRun(bg, "default", sid, "o1", "a")
	b, err := s.BeginCapRun(bg, "default", sid, "o2", "b") // the same mode joins
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndCapRun(bg, "default", a.ID, "o1"); err != nil {
		t.Fatal(err)
	}
	if _, err := rexec(t, s, sid, "o3", "e1", 1); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("one cap run still in flight: %v", err)
	}
	if err := s.EndCapRun(bg, "default", b.ID, "o2"); err != nil {
		t.Fatal(err)
	}
	if _, err := rexec(t, s, sid, "o3", "e1", 1); err != nil {
		t.Fatalf("drained: %v", err)
	}
}

// Another session is its own mode.
func TestTheModeIsPerSession(t *testing.T) {
	s, db, _ := newTestStore(t)
	a, b := rsession(t, s, db, 5), rsession(t, s, db, 5)
	if _, err := s.BeginCapRun(bg, "default", a, "o1", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := rexec(t, s, b, "o2", "e1", 1); err != nil {
		t.Fatalf("%v", err)
	}
}

// A cap run whose lease lapsed past the grace is dead: it keeps nothing out.
func TestLapsedCapRunsDoNotLatch(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 5)
	if _, err := s.BeginCapRun(bg, "default", sid, "o1", "a"); err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease + ReservationGrace - time.Second)
	if _, err := rexec(t, s, sid, "o2", "e1", 1); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("still within the grace: %v", err)
	}
	clk.advance(2 * time.Second)
	if _, err := rexec(t, s, sid, "o2", "e1", 1); err != nil {
		t.Fatalf("lapsed: %v", err)
	}
}

func TestSweepOfLapsedCapRunsAtRegistration(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 5)
	for i := 0; i < 3; i++ {
		if _, err := s.BeginCapRun(bg, "default", sid, "dead", "x"); err != nil {
			t.Fatal(err)
		}
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.BeginCapRun(bg, "default", sid, "o", "y"); err != nil {
		t.Fatal(err)
	}
	if n := capRows(t, s, sid); n != 1 {
		t.Fatalf("%d rows, want the one live run", n)
	}
}

func TestCapRunHeartbeatAndFences(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 5)
	r, _ := s.BeginCapRun(bg, "default", sid, "o1", "a")
	if err := s.HeartbeatCapRun(bg, "default", r.ID, "stranger"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("stranger heartbeat: %v", err)
	}
	if err := s.EndCapRun(bg, "default", r.ID, "stranger"); err != nil || capRows(t, s, sid) != 1 {
		t.Fatalf("a stranger ended it: %v", err)
	}
	clk.advance(ReservationLease - time.Second)
	if err := s.HeartbeatCapRun(bg, "default", r.ID, "o1"); err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease + ReservationGrace - time.Second) // past the first lease and grace, within the renewed one
	if _, err := rexec(t, s, sid, "o2", "e1", 1); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("a renewed cap run latches: %v", err)
	}
	if err := s.EndCapRun(bg, "default", r.ID, "o1"); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatCapRun(bg, "default", r.ID, "o1"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("heartbeat of an ended run: %v", err)
	}
}

// A cap run that lapsed past the grace (a suspended machine) and finds a reservation on the session on waking has lost
// its place: it must stop, not run on beside the hold. Without a reservation it is renewed as a reservation would be.
func TestLapsedCapRunLosesToAReservationButSurvivesWithout(t *testing.T) {
	s, db, clk := newTestStore(t)
	sid := rsession(t, s, db, 5)
	a, _ := s.BeginCapRun(bg, "default", sid, "o1", "a")
	other, _ := s.BeginCapRun(bg, "default", rsession(t, s, db, 5), "o1", "b")
	clk.advance(ReservationLease + ReservationGrace + time.Minute)
	if err := s.HeartbeatCapRun(bg, "default", other.ID, "o1"); err != nil {
		t.Fatalf("lapsed, nothing else on the session: %v", err)
	}
	if _, err := rexec(t, s, sid, "o2", "e1", 2); err != nil { // the lapsed cap run is ignored
		t.Fatal(err)
	}
	if err := s.HeartbeatCapRun(bg, "default", a.ID, "o1"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("it must stop: %v", err)
	}
	if err := s.HeartbeatCapRun(bg, "default", a.ID, "o1"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("and it stays lost: %v", err)
	}
}

func TestCapRunLimitAndCascade(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 5)
	for i := 0; i < MaxCapRunsPerSession; i++ {
		if _, err := s.BeginCapRun(bg, "default", sid, "o", "r"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.BeginCapRun(bg, "default", sid, "o", "r"); !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	if _, err := db.Exec(`DELETE FROM task_sessions WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if capRows(t, s, sid) != 0 {
		t.Fatal("the rows outlived the session")
	}
}

func TestCapRunInputs(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 5)
	for _, c := range [][2]string{{"", "r"}, {"o", ""}, {"o\x00", "r"}, {"o", "r\x00"}} {
		if _, err := s.BeginCapRun(bg, "default", sid, c[0], c[1]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", c, err)
		}
	}
	if _, err := s.BeginCapRun(bg, "default", sid+99, "o", "r"); err == nil {
		t.Fatal("no such session")
	}
	addProfile(t, db, "other")
	if _, err := s.BeginCapRun(bg, "other", sid, "o", "r"); err == nil {
		t.Fatal("another profile's session")
	}
}

// The default ask: a run that names no limit of its own on a budgeted session takes min(ask, left), not everything.
func TestReserveExecAsk(t *testing.T) {
	s, db, _ := newTestStore(t)
	sid := rsession(t, s, db, 10)
	r, err := s.ReserveExecAsk(bg, "default", sid, "o1", "w1", "e1", 0, 1)
	if err != nil || !r.Bounded || !near(r.AmountUSD, 1) {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err = s.ReserveExecAsk(bg, "default", sid, "o1", "w2", "e1", 4, 1); err != nil || !near(r.AmountUSD, 4) {
		t.Fatalf("a named limit wins: %+v %v", r, err)
	}
	free := rsession(t, s, db, 0)
	if r, err = s.ReserveExecAsk(bg, "default", free, "o1", "w1", "e1", 0, 1); err != nil || r.Bounded || r.AmountUSD != 0 {
		t.Fatalf("no budget, no cap: %+v %v", r, err)
	}
	if _, err = db.Exec(`UPDATE task_sessions SET budget_usd = 5.005 WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveExecAsk(bg, "default", sid, "o1", "w3", "e1", 0, 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("nothing left: %v", err)
	}
	// a session with none of its own under a budgeted parent is bounded by the parent, and asks for the ask
	kid := rkid(t, s, db, rsession(t, s, db, 20), 0, "running")
	if r, err = s.ReserveExecAsk(bg, "default", kid, "o1", "w1", "e1", 0, 1); err != nil || !r.Bounded || !near(r.AmountUSD, 1) {
		t.Fatalf("%+v %v", r, err)
	}
}
