package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// processes opens k stores, each over its own *sql.DB on one database file, standing in for k processes (each
// gets its own owner id from the caller). The first store's database is the one the test seeds.
func processes(t *testing.T, k int) []*Store {
	t.Helper()
	path := testdb.Path(t)
	var out []*Store
	for i := 0; i < k; i++ {
		d, err := storage.NewDatabase(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		out = append(out, NewStore(d.DB))
	}
	return out
}

func seedBudget(t *testing.T, db *sql.DB, budget float64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO task_sessions (profile_id, title, budget_usd, created_by, created_at, updated_at) VALUES ('default', 'race', ?, 'you', ?, ?)`, budget, rowTime, rowTime)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// I2: reservers in separate "processes" never get more than left in total, and what is left is all given out.
func TestConcurrentReserversNeverGetMoreThanLeft(t *testing.T) {
	const k, each = 8, 3
	stores := processes(t, k)
	session := seedBudget(t, stores[0].db, 10)
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
				r, err := stores[p].Reserve(bg, "default", session, owner, fmt.Sprintf("w%d", i), 1.7)
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
	if granted > 10+1e-9 {
		t.Fatalf("granted %v of a budget of 10", granted)
	}
	if granted < 10-0.01 {
		t.Fatalf("granted only %v of 10 although every caller wanted more", granted)
	}
	c, err := stores[0].Commitment(bg, "default", session)
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := c.Left(); !near(c.Held, granted) || left > 0.01 {
		t.Fatalf("commitment %+v vs granted %v", c, granted)
	}
}

// Reserve, heartbeat, settle and expire racing from separate handles keep the money whole: every end is the
// owner's, nothing is charged twice, and the session never goes above its budget.
func TestConcurrentLifecyclesKeepTheBudgetWhole(t *testing.T) {
	const k = 6
	stores := processes(t, k)
	session := seedBudget(t, stores[0].db, 12)
	var wg sync.WaitGroup
	var mu sync.Mutex
	settled := 0.0
	for p := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := fmt.Sprintf("proc-%d", p)
			for i := 0; i < 4; i++ {
				r, err := stores[p].Reserve(bg, "default", session, owner, "w", 2)
				if err != nil {
					continue
				}
				if err := stores[p].Heartbeat(bg, "default", r.ID, owner); err != nil {
					t.Errorf("heartbeat: %v", err)
				}
				// a stranger from another handle can neither renew nor end it
				if err := stores[(p+1)%k].Settle(bg, "default", r.ID, "stranger", 1); !errors.Is(err, ErrReservationLost) {
					t.Errorf("stranger settle: %v", err)
				}
				if _, err := stores[p].ExpireStale(bg, "default", session); err != nil {
					t.Errorf("expire: %v", err)
				}
				if err := stores[p].Settle(bg, "default", r.ID, owner, 1.5); err != nil {
					t.Errorf("settle: %v", err)
					continue
				}
				mu.Lock()
				settled += 1.5
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	c, err := stores[0].Commitment(bg, "default", session)
	if err != nil {
		t.Fatal(err)
	}
	if !near(c.Spent, settled) || c.Held != 0 || c.Total() > 12+1e-9 {
		t.Fatalf("commitment %+v after settling %v", c, settled)
	}
}

// A crash, across handles: the holder's handle is dropped, a different process sweeps it, and the row keeps holding.
func TestCrashAcrossProcessesKeepsTheMoneyHeld(t *testing.T) {
	stores := processes(t, 2)
	session := seedBudget(t, stores[0].db, 10)
	clk := newClock()
	stores[0].now, stores[1].now = clk.now, clk.now
	r, err := stores[0].Reserve(bg, "default", session, "dies", "w", 7)
	if err != nil {
		t.Fatal(err)
	}
	// stores[0] is never used again for this reservation: the "process" is gone
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if n, err := stores[1].ExpireStale(bg, "default", session); err != nil || n != 1 {
		t.Fatalf("%d, %v", n, err)
	}
	c, err := stores[1].Commitment(bg, "default", session)
	if err != nil || !near(c.Held, 7) {
		t.Fatalf("%+v, %v", c, err)
	}
	if err := stores[1].Heartbeat(bg, "default", r.ID, "other"); !errors.Is(err, ErrReservationLost) {
		t.Fatalf("%v", err)
	}
}
