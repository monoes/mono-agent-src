package tasks

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// TestAccountingHelper is not a test: the kill -9 tests below run it in another process on the same database file. It
// takes its hold, says what it holds, and then waits for ever (until it is killed).
func TestAccountingHelper(t *testing.T) {
	if os.Getenv("ACCT_HELPER") != "1" {
		t.Skip("the helper of the tests that kill a process")
	}
	db, err := storage.NewDatabase(os.Getenv("ACCT_HELPER_DB"))
	if err != nil {
		fmt.Println("ERROR", err)
		os.Exit(1)
	}
	s := NewStore(db.DB)
	session, _ := strconv.ParseInt(os.Getenv("ACCT_HELPER_SESSION"), 10, 64)
	switch os.Getenv("ACCT_HELPER_MODE") {
	case "reserve":
		r, err := s.ReserveExec(bg, "default", session, "doomed-owner", "doomed", "e1", 2)
		if err == nil {
			err = s.ChargeExec(bg, "default", r.ID, "doomed-owner", 0.4, false)
		}
		if err != nil {
			fmt.Println("ERROR", err)
			os.Exit(1)
		}
		fmt.Println("HELD", r.ID)
	case "cap":
		r, err := s.BeginCapRun(bg, "default", session, "doomed-owner", "doomed")
		if err != nil {
			fmt.Println("ERROR", err)
			os.Exit(1)
		}
		fmt.Println("HELD", r.ID)
	}
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // never answered: the parent kills this process
	os.Exit(0)
}

// killedHolder starts a helper that takes a hold in the given mode, waits until it has it, and kills it with SIGKILL.
func killedHolder(t *testing.T, path, mode string, session int64) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAccountingHelper$")
	cmd.Env = append(os.Environ(), "ACCT_HELPER=1", "ACCT_HELPER_DB="+path, "ACCT_HELPER_MODE="+mode, fmt.Sprintf("ACCT_HELPER_SESSION=%d", session))
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if !strings.HasPrefix(line, "HELD") {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("the helper said %q (%v)", line, err)
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL: no deferred function, no cleanup, no goodbye
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

// offsetClock is real time plus a shift the test controls: the helper's rows carry real stamps.
type offsetClock struct{ shift atomic.Int64 }

func (c *offsetClock) now() time.Time { return time.Now().Add(time.Duration(c.shift.Load())) }

// A process killed while it holds a reservation (and has charged part of it): its money stays held and its mode stays
// latched (no cap run enters) until the lease and the grace have passed, then the row is expired and STILL holds what is
// left until the operator resolves it; the cost it charged is never lost, and nothing is ever committed past the budget.
func TestKillNineWhileReservedKeepsTheMoneyHeldAndTheModeLatched(t *testing.T) {
	stores := processes(t, 1)
	s := stores[0]
	clk := &offsetClock{}
	s.now = clk.now
	session := seedBudget(t, s.db, 5)
	killedHolder(t, testPathOf(t, s), "reserve", session)

	c := rcommit(t, s, session)
	if !near(c.Spent, 0.4) || !near(c.Held, 1.6) {
		t.Fatalf("right after the kill: %+v (charged 0.4 of a grant of 2)", c)
	}
	if _, err := s.BeginCapRun(bg, "default", session, "o2", "r"); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("the dead holder's reservation latches the mode until its lease lapses: %v", err)
	}
	r2, err := s.ReserveExec(bg, "default", session, "o2", "w2", "e1", 4)
	if err != nil || !near(r2.AmountUSD, 3) { // 5 less the 0.4 spent and the 1.6 still held
		t.Fatalf("its money is still held, so the next grant is 3, not 4: %+v %v", r2, err)
	}
	if err := s.ReleaseReservation(bg, "default", r2.ID, "o2"); err != nil {
		t.Fatal(err)
	}
	clk.shift.Store(int64(ReservationLease + ReservationGrace + time.Second))
	if n, err := s.ExpireStale(bg, "default", session); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if c = rcommit(t, s, session); !near(c.Spent, 0.4) || !near(c.Held, 1.6) {
		t.Fatalf("expired, it still holds what is left: %+v", c)
	}
	if _, err := s.BeginCapRun(bg, "default", session, "o2", "r"); err != nil {
		t.Fatalf("an expired row is not in flight: the cap mode may enter (its cap subtracts the hold): %v", err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM task_session_reservations WHERE session_id = ?`, session).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveReservation(bg, "default", id, 0.7, "killed", human); err != nil {
		t.Fatal(err)
	}
	if c = rcommit(t, s, session); !near(c.Spent, 0.7) || c.Held != 0 || c.Total() > 5+1e-9 {
		t.Fatalf("after the operator's resolve: %+v", c)
	}
}

// A process killed while a run is registered on the cap: the registration keeps every reservation off the session
// until its lease and the grace have lapsed, and no longer.
func TestKillNineWhileRegisteredOnTheCapLatchesUntilTheLeaseLapses(t *testing.T) {
	s := processes(t, 1)[0]
	clk := &offsetClock{}
	s.now = clk.now
	session := seedBudget(t, s.db, 5)
	killedHolder(t, testPathOf(t, s), "cap", session)
	if _, err := s.ReserveExec(bg, "default", session, "o2", "w2", "e1", 1); !errors.Is(err, ErrAccountingMode) {
		t.Fatalf("right after the kill the run may still be alive: %v", err)
	}
	clk.shift.Store(int64(ReservationLease + ReservationGrace + time.Second))
	if _, err := s.ReserveExec(bg, "default", session, "o2", "w2", "e1", 1); err != nil {
		t.Fatalf("lapsed: %v", err)
	}
}

func testPathOf(t *testing.T, s *Store) string {
	t.Helper()
	var seq int
	var name, file string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
