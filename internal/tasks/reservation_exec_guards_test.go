package tasks

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

// Guards of the exec API that the main tests do not reach: the shape rules of the cost record that live in the
// table, a row that was not taken for an exec, a raw replace of an exec's row, and a race on one reservation.

const insertReport = `INSERT INTO task_session_reservation_costs (reservation_id, seq, cumulative_usd, final, reported_at) VALUES (?, ?, ?, ?, ?)`

func TestChargeExecRefusesARowNotTakenForAnExec(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	legacy := rreserve(t, s, id, "o", 4) // taken by Reserve: exec_id is ''
	if err := s.ChargeExec(bg, "default", legacy.ID, "o", 1, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("charge: %v", err)
	}
	if err := s.SettleExecNoReport(bg, "default", legacy.ID, "o"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("settle: %v", err)
	}
	if row := rrow(t, s, legacy.ID); row.State != ReservationLive || row.ChargedUSD != 0 || rspent(t, db, id) != 0 {
		t.Fatalf("a refused call changed %+v", row)
	}
}

// Each rule of the cost record is the table's own, so a writer that bypasses the store meets it.
func TestCostRecordTriggersKeepItsShape(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	legacy := rreserve(t, s, id, "o", 2)
	exec := xreserve(t, s, id, "o", "w", "e1", 2)
	ended := xreserve(t, s, id, "o", "w", "e2", 2)
	if err := s.ChargeExec(bg, "default", ended.ID, "o", 1, true); err != nil {
		t.Fatal(err)
	}
	mustFail := func(what string, args ...any) {
		t.Helper()
		if _, err := db.Exec(insertReport, args...); err == nil {
			t.Errorf("accepted: %s", what)
		}
	}
	mustFail("a report on a hold taken by Reserve", legacy.ID, 1, 1.0, 0, rowTime)
	mustFail("a report on an ended exec", ended.ID, 2, 1.0, 0, rowTime)
	mustFail("a report on a reservation that does not exist", 99999, 1, 1.0, 0, rowTime)
	mustFail("the first report numbered 5", exec.ID, 5, 1.0, 0, rowTime)
	mustFail("a first report numbered 0", exec.ID, 0, 1.0, 0, rowTime)
	if _, err := db.Exec(insertReport, exec.ID, 1, 1.0, 1, rowTime); err != nil { // a final report, written by hand
		t.Fatalf("a first report: %v", err)
	}
	mustFail("a report after the final one", exec.ID, 2, 3.0, 0, rowTime)
	mustFail("a duplicate number", exec.ID, 1, 3.0, 0, rowTime)
}

// A replace of the row of an exec by raw SQL would drop the hold and its cost record without the delete rules
// running. The table refuses every form of it, and the original hold is as it was.
func TestRawReplaceOfAnExecReservationIsRefused(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	r := xreserve(t, s, id, "o", "w", "e1", 4)
	if err := s.ChargeExec(bg, "default", r.ID, "o", 3, false); err != nil {
		t.Fatal(err)
	}
	cols := `(profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at, exec_id)`
	vals := `VALUES ('default', ?, 'w', 'thief', 0.01, 1, 'live', ?, ?, ?, 'e1')`
	for _, stmt := range []string{
		`INSERT OR REPLACE INTO task_session_reservations ` + cols + ` ` + vals,
		`REPLACE INTO task_session_reservations ` + cols + ` ` + vals,
		`INSERT INTO task_session_reservations ` + cols + ` ` + vals + ` ON CONFLICT DO UPDATE SET owner_id = 'thief'`,
		`INSERT OR IGNORE INTO task_session_reservations ` + cols + ` ` + vals,
	} {
		if _, err := db.Exec(stmt, id, rowTime, rowTime, rowTime); err == nil {
			t.Errorf("accepted: %s", stmt)
		}
	}
	row := rrow(t, s, r.ID)
	if row.OwnerID != "o" || !near(row.AmountUSD, 4) || !near(row.ChargedUSD, 3) || xcount(t, db, id) != 1 {
		t.Fatalf("the hold changed: %+v", row)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_session_reservation_costs WHERE reservation_id = ?`, r.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d reports left, %v", n, err)
	}
}

// Many callers of separate handles report on one reservation at once. The charge never shrinks (a watcher reads
// it throughout), and what it ends at is the largest report, in the row and in the session's spend, whatever
// order the reports ran in and however low the final one is.
func TestConcurrentChargeExecOnOneReservationEndsAtTheMax(t *testing.T) {
	const k, each = 6, 25
	stores := processes(t, k+1)
	session := seedBudget(t, stores[0].db, 1000)
	r, err := stores[0].ReserveExec(bg, "default", session, "o", "w", "e1", 500)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	maxReport := make([]float64, k)
	for p := 0; p < k; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(p) + 1))
			for i := 0; i < each; i++ {
				v := float64(rng.Intn(400_000)) / 1000 // up to 400 dollars, whole milli-dollars
				maxReport[p] = max(maxReport[p], v)
				if err := stores[p].ChargeExec(bg, "default", r.ID, "o", v, false); err != nil {
					t.Errorf("proc %d: %v", p, err)
					return
				}
			}
		}()
	}
	stop := make(chan struct{})
	watched := make(chan error, 1)
	go func() {
		last := 0.0
		for {
			select {
			case <-stop:
				watched <- nil
				return
			default:
			}
			var c float64
			if err := stores[k].db.QueryRow(`SELECT charged_usd FROM task_session_reservations WHERE id = ?`, r.ID).Scan(&c); err != nil {
				watched <- err
				return
			}
			if c < last {
				watched <- fmt.Errorf("the charge went from %v down to %v", last, c)
				return
			}
			last = c
		}
	}()
	wg.Wait()
	close(stop)
	if err := <-watched; err != nil {
		t.Fatal(err)
	}
	want := 0.0
	for _, m := range maxReport {
		want = max(want, m)
	}
	if got := rrow(t, stores[0], r.ID).ChargedUSD; !near(got, want) || !near(rspent(t, stores[0].db, session), want) {
		t.Fatalf("charged %v spent %v, want the largest report %v", got, rspent(t, stores[0].db, session), want)
	}
	if err := stores[1].ChargeExec(bg, "default", r.ID, "o", 0.5, true); err != nil { // a low final changes nothing
		t.Fatal(err)
	}
	row := rrow(t, stores[0], r.ID)
	if row.State != ReservationSettled || !near(row.ChargedUSD, want) || !near(rspent(t, stores[0].db, session), want) {
		t.Fatalf("%+v spent %v, want %v", row, rspent(t, stores[0].db, session), want)
	}
}
