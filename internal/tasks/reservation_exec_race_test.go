package tasks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

const (
	execHelperDB      = "TASKS_EXEC_HELPER_DB"
	execHelperSession = "TASKS_EXEC_HELPER_SESSION"
	execHelperName    = "TASKS_EXEC_HELPER_NAME"
	helperTries       = 3 // each of 4 helpers asks 3 times for 1.7: 20.4 asked of 10
)

// grantsNeverPassTheBudget checks the books after a race for the last dollars: what was granted in all is the
// session's budget, no more, and the commitment agrees with the rows.
func grantsNeverPassTheBudget(t *testing.T, s *Store, session int64, budget, granted float64) {
	t.Helper()
	if granted > budget+1e-9 {
		t.Fatalf("granted %v of a budget of %v", granted, budget)
	}
	if granted < budget-0.01 {
		t.Fatalf("granted only %v of %v although every caller wanted more", granted, budget)
	}
	c := rcommit(t, s, session)
	if left, _ := c.Left(); !near(c.Held, granted) || left > 0.01 || left < -1e-9 {
		t.Fatalf("commitment %+v vs granted %v", c, granted)
	}
}

// Reservers of separate handles on one database file (standing in for processes) never get more than is left
// in total, with a retry of every exec thrown in: a retry is the same hold.
func TestConcurrentReserveExecNeverGrantsMoreThanLeft(t *testing.T) {
	const k, each = 8, 3
	stores := processes(t, k)
	session := seedBudget(t, stores[0].db, 10)
	var mu sync.Mutex
	rows := map[int64]float64{}
	var wg sync.WaitGroup
	errs := make(chan error, 2*k*each)
	for p := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := fmt.Sprintf("proc-%d", p)
			for i := 0; i < each; i++ {
				for try := 0; try < 2; try++ { // the second call is the retry of the first
					r, err := stores[p].ReserveExec(bg, "default", session, owner, fmt.Sprintf("w%d", p), fmt.Sprintf("e%d", i), 1.7)
					switch {
					case err == nil:
						mu.Lock()
						rows[r.ID] = r.AmountUSD
						mu.Unlock()
					case errors.Is(err, ErrLimit):
					default:
						errs <- err
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("reserve exec: %v", err)
	}
	granted := 0.0
	for _, a := range rows {
		granted += a
	}
	grantsNeverPassTheBudget(t, stores[0], session, 10, granted)
}

// Two callers racing to reserve the SAME exec get the same row.
func TestConcurrentReserveExecOfOneExecMakesOneHold(t *testing.T) {
	const k = 8
	stores := processes(t, k)
	session := seedBudget(t, stores[0].db, 10)
	ids := make([]int64, k)
	var wg sync.WaitGroup
	for p := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := stores[p].ReserveExec(bg, "default", session, "same-owner", "w", "e1", 3)
			if err != nil {
				t.Errorf("proc %d: %v", p, err)
				return
			}
			ids[p] = r.ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids %v: one exec, several holds", ids)
		}
	}
	if n := xcount(t, stores[0].db, session); n != 1 {
		t.Fatalf("%d rows", n)
	}
	if c := rcommit(t, stores[0], session); !near(c.Held, 3) {
		t.Fatalf("%+v", c)
	}
}

// TestExecReserveHelper is not a test: TestReserveExecFromOSProcesses runs it in other processes that share one
// database file with the test. A helper says READY, waits for "go", then asks for the budget in 1.7 dollar bites,
// helperTries times, and prints GRANT <id> <amount> for each hold it got.
func TestExecReserveHelper(t *testing.T) {
	path := os.Getenv(execHelperDB)
	if path == "" {
		t.Skip("the helper of TestReserveExecFromOSProcesses")
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		fmt.Println("ERROR", err)
		os.Exit(1)
	}
	defer db.Close()
	s := claimsSlowStore(db) // the clock is asked inside the write lock and takes a moment: the helpers are in each other's way
	session, _ := strconv.ParseInt(os.Getenv(execHelperSession), 10, 64)
	name := os.Getenv(execHelperName)
	fmt.Println("READY")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		os.Exit(1)
	}
	// "go AT": every helper begins at the same instant (AT, nanoseconds since the epoch), so they really collide
	if f := strings.Fields(line); len(f) == 2 {
		if at, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			time.Sleep(time.Until(time.Unix(0, at)))
		}
	}
	for i := 0; i < helperTries; i++ {
		r, err := s.ReserveExec(context.Background(), "default", session, name, "w-"+name, fmt.Sprintf("e%d", i), 1.7)
		if errors.Is(err, ErrLimit) {
			continue
		}
		if err != nil {
			fmt.Println("ERROR", err)
			os.Exit(1)
		}
		fmt.Printf("GRANT %d %v\n", r.ID, r.AmountUSD)
	}
}

// The last dollars, raced for by real OS processes on one database file: the grants add up to the budget.
func TestReserveExecFromOSProcesses(t *testing.T) {
	const k = 4
	stores := processes(t, 1) // a store over a fresh migrated file; the helpers open the same file
	session := seedBudget(t, stores[0].db, 10)
	var dbPath string
	if err := stores[0].db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 2*time.Minute)
	defer cancel()
	cmds := make([]*exec.Cmd, k)
	ins := make([]io.WriteCloser, k)
	outs := make([]*bufio.Reader, k)
	errb := make([]*claimsOutput, k)
	race := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
	for i := range cmds {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecReserveHelper$")
		cmd.Env = append(os.Environ(), execHelperDB+"="+dbPath, execHelperSession+"="+strconv.FormatInt(session, 10),
			fmt.Sprintf("%s=proc-%d", execHelperName, i), "GORACE="+race)
		errb[i] = &claimsOutput{}
		cmd.Stderr = errb[i]
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i], ins[i], outs[i] = cmd, in, bufio.NewReader(out)
	}
	for i, out := range outs {
		if line, err := out.ReadString('\n'); strings.TrimSpace(line) != "READY" {
			t.Fatalf("helper %d said %q (%v); its stderr:\n%s", i, line, err, errb[i])
		}
	}
	at := time.Now().Add(2 * time.Second).UnixNano()
	for i, in := range ins {
		if _, err := fmt.Fprintf(in, "go %d\n", at); err != nil {
			t.Fatalf("helper %d: %v; its stderr:\n%s", i, err, errb[i])
		}
	}
	granted, seen := 0.0, map[string]bool{}
	for i, out := range outs {
		rest, err := io.ReadAll(out)
		if err != nil {
			t.Fatalf("helper %d: %v", i, err)
		}
		if err := cmds[i].Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s\nits stderr:\n%s", i, err, rest, errb[i])
		}
		mine := 0
		defer func() { t.Logf("helper %d got %d holds", i, mine) }()
		for _, l := range strings.Split(string(rest), "\n") {
			f := strings.Fields(l)
			if len(f) == 3 && f[0] == "GRANT" {
				a, _ := strconv.ParseFloat(f[2], 64)
				if seen[f[1]] {
					t.Fatalf("hold #%s granted twice", f[1])
				}
				seen[f[1]] = true
				mine++
				granted += a
			}
		}
	}
	grantsNeverPassTheBudget(t, stores[0], session, 10, granted)
	if n := xcount(t, stores[0].db, session); n != len(seen) {
		t.Fatalf("%d rows, %d grants", n, len(seen))
	}
}
