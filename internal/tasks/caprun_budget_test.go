package tasks

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// A budget write and the registration of a cap run on the same session serialize in the store's write transaction: they
// never both succeed with the run registered first and the budget lowered after it. A trigger records the order in which
// the two rows were written (the stamps are whole seconds).
func TestBeginCapRunRacingALoweringNeverEndsWithBothAfterTheRun(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint("pass", seed), func(t *testing.T) {
			stores := processes(t, 2)
			db := stores[0].db
			for _, q := range []string{
				`CREATE TABLE ord (seq INTEGER PRIMARY KEY AUTOINCREMENT, what TEXT, session_id INTEGER)`,
				`CREATE TRIGGER ord_run AFTER INSERT ON task_session_cap_runs BEGIN INSERT INTO ord (what, session_id) VALUES ('run', NEW.session_id); END`,
				`CREATE TRIGGER ord_set AFTER UPDATE OF budget_usd ON task_sessions WHEN NEW.budget_usd < OLD.budget_usd BEGIN INSERT INTO ord (what, session_id) VALUES ('set', NEW.id); END`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			const rounds = 25
			var both, runFirst, setFirst int
			for i := 0; i < rounds; i++ {
				session := seedBudget(t, db, 10)
				var runErr, setErr error
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					_, runErr = stores[0].BeginCapRun(bg, "default", session, "o", "r")
				}()
				go func() {
					defer wg.Done()
					<-start
					_, setErr = setBudget(stores[1], session, 4)
				}()
				close(start)
				wg.Wait()
				if runErr != nil || (setErr != nil && !errors.Is(setErr, ErrAccountingMode)) {
					t.Fatalf("round %d: run %v, set %v", i, runErr, setErr)
				}
				if setErr != nil {
					runFirst++
					continue
				}
				both++
				var first string
				if err := db.QueryRow(`SELECT what FROM ord WHERE session_id = ? ORDER BY seq LIMIT 1`, session).Scan(&first); err != nil {
					t.Fatal(err)
				}
				if first == "run" {
					t.Fatalf("round %d: the budget was lowered while a registered run was live", i)
				}
				setFirst++
			}
			t.Logf("seed %d: run first %d, set first %d, both %d", seed, runFirst, setFirst, both)
		})
	}
}

// The helper lists the live runs of the session and of the sessions below it: not those of a sibling or of an ancestor,
// and not a run whose lease lapsed past the grace.
func TestLiveCapRunsBelowListsTheSubtreeOnly(t *testing.T) {
	s, db, clk := newTestStore(t)
	root := rsession(t, s, db, 20)
	a := rkid(t, s, db, root, 6, "running")
	b := rkid(t, s, db, root, 6, "running")
	grand := rkid(t, s, db, a, 4, "running")
	list := func(id int64) []CapRun {
		var out []CapRun
		err := s.tx(bg, func(x dbx) (e error) { out, e = s.liveCapRunsBelowTx(bg, x, "default", id); return })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	old, err := s.BeginCapRun(bg, "default", b, "o", "dying")
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(ReservationLease + ReservationGrace + time.Second)
	if _, err := s.BeginCapRun(bg, "default", root, "o", "root-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCapRun(bg, "default", grand, "o", "grand-run"); err != nil {
		t.Fatal(err)
	}
	if got := list(a); len(got) != 1 || got[0].RunRef != "grand-run" {
		t.Fatalf("below a: %+v", got)
	}
	if got := list(root); len(got) != 2 {
		t.Fatalf("below root (the lapsed run of b counts for nothing): %+v", got)
	}
	if got := list(b); len(got) != 0 {
		t.Fatalf("b has only a lapsed run: %+v (%d)", got, old.ID)
	}
}
