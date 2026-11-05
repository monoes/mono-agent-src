package coderhost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// carveFix is a capped master with one uncapped child row, for tests that drive the carve transaction directly.
func carveFix(t *testing.T) (st *Store, parent, child string) {
	t.Helper()
	st = newStoreT(t)
	m, _ := master(t, st, "coding")
	if err := st.SetBudget("p", m.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	return st, m.ConversationID, spawnRaw(t, st, m)
}

func childBudget(t *testing.T, st *Store, child string) float64 {
	t.Helper()
	b, err := st.Budget("p", child)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each guard of the admission is pinned alone: a stub that refuses on one side only.
func TestCarveRefusedByTheBeforeSideAloneIsUndone(t *testing.T) {
	st, m, k := carveFix(t)
	err := st.carveBudgetAdmit("p", m, k, 4, func(_ querier, _ float64, written bool) (string, error) {
		if !written {
			return "no room above", nil
		}
		return "", nil
	})
	if !errors.Is(err, errCarveNoRoom) || !strings.Contains(err.Error(), "no room above") {
		t.Fatalf("err = %v, want errCarveNoRoom with the reason", err)
	}
	if b := childBudget(t, st, k); b != 0 {
		t.Errorf("the child holds $%v after a refused carve", b)
	}
}

func TestCarveRefusedByTheAfterSideAloneIsUndone(t *testing.T) {
	st, m, k := carveFix(t)
	err := st.carveBudgetAdmit("p", m, k, 4, func(q querier, _ float64, written bool) (string, error) {
		if !written {
			return "", nil
		}
		var b float64
		if e := q.QueryRow(`SELECT budget_usd FROM coder_sessions WHERE conversation_id = ?`, k).Scan(&b); e != nil || b != 4 {
			t.Errorf("the after side must see the carve in the rows: %v %v", b, e)
		}
		return "committed past a budget", nil
	})
	if !errors.Is(err, errCarveNoRoom) || !strings.Contains(err.Error(), "committed past a budget") {
		t.Fatalf("err = %v, want errCarveNoRoom with the reason", err)
	}
	if b := childBudget(t, st, k); b != 0 {
		t.Errorf("the carve stayed after the after side refused: $%v", b)
	}
}

// A writer that commits between the admission's read and the carve's write must not fail the spawn: the carve
// takes the write lock before its first read.
func TestCarveSurvivesAWriterBetweenTheReadAndTheWrite(t *testing.T) {
	st, m, k := carveFix(t)
	st.db.SetMaxOpenConns(8)
	competed := false
	err := st.carveBudgetAdmit("p", m, k, 4, func(q querier, _ float64, written bool) (string, error) {
		if written {
			return "", nil
		}
		var n int
		if e := q.QueryRow(`SELECT COUNT(*) FROM coder_sessions`).Scan(&n); e != nil {
			return "", e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, e := st.db.ExecContext(ctx, `UPDATE coder_sessions SET updated_at = 'competitor' WHERE conversation_id = ?`, m)
		competed = e == nil // with the write lock held up front the competitor waits (and gives up); otherwise it commits
		return "", nil
	})
	if err != nil {
		t.Fatalf("the carve failed under a competing writer (competitor committed: %v): %v", competed, err)
	}
	if b := childBudget(t, st, k); b != 4 {
		t.Errorf("child budget = %v, want 4", b)
	}
}

// (2) The reason of a refusal caused by a budgeted ancestor reaches the lead, and (3) the refusal closes the spawn
// intent so the same lead can spawn again.
func TestRefusedSpawnKeepsItsReasonClosesTheIntentAndLetsTheLeadSpawnAgain(t *testing.T) {
	f := newMC(t, true)
	setSessionBudget(t, f, 10)
	once := true
	f.st.SetDispatchGate(&handFunc{before: func() {
		if once {
			once = false
			if _, err := f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 8); err != nil {
				t.Errorf("the competing reservation: %v", err)
			}
		}
	}})
	r := req("Docs", f.bigScope())
	r.BudgetUSD = 4
	_, err := f.spawnSession(r)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
	if !strings.Contains(err.Error(), "does not fit the budget of your session or of a session above it") {
		t.Errorf("the reason of the refusal was dropped: %v", err)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d spawn intents left open by the refusal", n)
	}
	r2 := req("Docs again", f.bigScope())
	r2.BudgetUSD = 0.5
	if _, err := f.spawnSession(r2); err != nil {
		t.Fatalf("the second spawn after a refusal: %v", err)
	}
	if why := f.overcommitted(t); why != "" {
		t.Errorf("committed past the budget: %s", why)
	}
}

// A spawn and a reservation racing for the last dollars: whichever wins, the other sees it. The reservation asks
// for $8 and is granted min(8, left): so if the spawn won it can hold at most $6, and if the reservation held
// more the spawn was refused. Neither result is ignored.
func TestConcurrentSpawnAndReservationAgreeOnWhoWon(t *testing.T) {
	for round := 0; round < 8; round++ {
		f := newMC(t, true)
		setSessionBudget(t, f, 10)
		r := req("Docs", f.bigScope())
		r.BudgetUSD = 4
		var (
			wg       sync.WaitGroup
			spawnErr error
			held     tasks.Reservation
			resErr   error
		)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, spawnErr = f.spawnSession(r) }()
		go func() {
			defer wg.Done()
			<-start
			held, resErr = f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 8)
		}()
		close(start)
		wg.Wait()
		if spawnErr != nil && !errors.Is(spawnErr, ErrLimit) {
			t.Fatalf("round %d: the spawn failed with %v", round, spawnErr)
		}
		if resErr != nil {
			t.Fatalf("round %d: the reservation failed with %v", round, resErr)
		}
		if spawnErr == nil && held.AmountUSD > 6+1e-9 {
			t.Fatalf("round %d: the spawn kept $4 and the reservation held $%v of the $10", round, held.AmountUSD)
		}
		if spawnErr != nil && held.AmountUSD > 6+1e-9 == false {
			t.Fatalf("round %d: the spawn was refused though the reservation held only $%v", round, held.AmountUSD)
		}
		if why := f.overcommitted(t); why != "" {
			t.Fatalf("round %d: committed past the budget: %s", round, why)
		}
	}
}
