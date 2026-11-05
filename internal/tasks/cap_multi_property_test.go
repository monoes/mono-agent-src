package tasks

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

// The multi-handle variant of TestCapRunPropertyInvariants: three store handles (three sql.DB connections to ONE database
// file, as three processes would have; the repo's helper-process pattern, TestAccountingHelper, only holds a hold and
// waits to be killed, so the handles here are in this process) run seeded random operation sequences AT THE SAME TIME,
// each with a clock of its own (an offsetClock: real time plus a shift the test moves between rounds, so one handle's
// leases lapse while another's do not). The tree is root $40 -> a $10 -> g $4, root -> b $8, all running.
//
// A model cannot follow the order of concurrent writes, so the invariants are recorded INSIDE the writing transactions
// by triggers, each judged by the clock of the writer (read from the row it wrote: created_at, heartbeat_at, the lease
// less ReservationLease, the event's at), and the test reads the verdicts and the final state after the run:
//
//	M1 no cap run is registered (or renewed) beside a live reservation of its session, and no reservation is
//	   granted beside a live cap run, in the eyes of the writer;
//	M4 no reservation that had lapsed past the grace is renewed beside a live cap run, in the eyes of the writer;
//	M2 no set-budget that lowers a budget (or sets one where there was none) is written while a cap run that is live in the
//	   eyes of the writer is on the session or below it;
//	M3 no double grant: after each grant the reservations of the session do not add up to more than its budget;
//	and, on the final database: no session commits more than its budget (Commitment.Floor), no cap run or reservation
//	is held by two owners at once (each id unique), at most MaxCapRunsPerSession cap runs per session.
//
// The renewal of a reservation that lapsed past the grace is drawn too; the trigger v_res_renew judges it (M4).
// Seeds 1 to 30, 14 rounds of 3 handles x 14 operations. A failure prints the seed, the verdict rows and each
// handle's operation list; the interleaving of the handles is the scheduler's, so a failure may need a few replays.
func TestCapRunMultiHandleInvariants(t *testing.T) {
	done := map[string]int{}
	for seed := int64(1); seed <= 30; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { runCapMulti(t, seed, done) })
	}
	t.Logf("operations that happened: %v", done)
	for _, k := range []string{"begin", "begin-refused", "reserve", "reserve-refused", "set", "lower-refused", "heartbeat-lost", "heartbeat-lapsed"} {
		if done[k] == 0 {
			t.Errorf("no %q ever happened: the test does not exercise it", k)
		}
	}
}

const capMultiTriggers = `
CREATE TABLE viol (seq INTEGER PRIMARY KEY AUTOINCREMENT, what TEXT);
CREATE TABLE pend (session_id INTEGER PRIMARY KEY);
CREATE TRIGGER v_run_in AFTER INSERT ON task_session_cap_runs BEGIN
  INSERT INTO viol(what) SELECT 'M1 cap run #'||NEW.id||' registered beside reservation #'||r.id||' on session #'||NEW.session_id
    FROM task_session_reservations r WHERE r.session_id = NEW.session_id AND r.state = 'live'
    AND r.lease_until >= strftime('%Y-%m-%dT%H:%M:%SZ', NEW.created_at, '-58 seconds');
END;
CREATE TRIGGER v_run_renew AFTER UPDATE OF lease_until ON task_session_cap_runs WHEN NEW.lease_until > OLD.lease_until BEGIN
  INSERT INTO viol(what) SELECT 'M1 cap run #'||NEW.id||' renewed beside reservation #'||r.id||' on session #'||NEW.session_id
    FROM task_session_reservations r WHERE r.session_id = NEW.session_id AND r.state = 'live'
    AND r.lease_until >= strftime('%Y-%m-%dT%H:%M:%SZ', NEW.lease_until, '-178 seconds');
END;
CREATE TRIGGER v_res_renew AFTER UPDATE OF lease_until ON task_session_reservations
  WHEN NEW.heartbeat_at <> OLD.heartbeat_at AND NEW.lease_until > OLD.lease_until
  AND OLD.lease_until < strftime('%Y-%m-%dT%H:%M:%SZ', NEW.heartbeat_at, '-62 seconds') BEGIN
  INSERT INTO viol(what) SELECT 'M4 lapsed reservation #'||NEW.id||' renewed beside cap run #'||c.id||' on session #'||NEW.session_id
    FROM task_session_cap_runs c WHERE c.session_id = NEW.session_id
    AND c.lease_until >= strftime('%Y-%m-%dT%H:%M:%SZ', NEW.heartbeat_at, '-58 seconds');
END;
CREATE TRIGGER v_res_in AFTER INSERT ON task_session_reservations BEGIN
  INSERT INTO viol(what) SELECT 'M1 reservation #'||NEW.id||' granted beside cap run #'||c.id||' on session #'||NEW.session_id
    FROM task_session_cap_runs c WHERE c.session_id = NEW.session_id
    AND c.lease_until >= strftime('%Y-%m-%dT%H:%M:%SZ', NEW.heartbeat_at, '-58 seconds');
  INSERT INTO viol(what) SELECT 'M3 session #'||NEW.session_id||' has reservations adding up to more than its budget'
    FROM task_sessions t WHERE t.id = NEW.session_id AND t.budget_usd > 0
    AND (SELECT SUM(amount_usd) FROM task_session_reservations WHERE session_id = NEW.session_id AND state IN ('live','expired')) > t.budget_usd + 1e-9;
END;
CREATE TRIGGER v_low AFTER UPDATE OF budget_usd ON task_sessions
  WHEN NEW.budget_usd > 0 AND (OLD.budget_usd <= 0 OR NEW.budget_usd < OLD.budget_usd) BEGIN
  INSERT OR REPLACE INTO pend(session_id) VALUES (NEW.id);
END;
CREATE TRIGGER v_low_at AFTER INSERT ON task_session_events
  WHEN NEW.kind IN ('limits', 'clamped') AND NEW.session_id IN (SELECT session_id FROM pend) BEGIN
  INSERT INTO viol(what) SELECT 'M2 budget of session #'||NEW.session_id||' lowered under live cap run #'||c.id||' on session #'||c.session_id
    FROM task_session_cap_runs c WHERE c.lease_until >= strftime('%Y-%m-%dT%H:%M:%SZ', NEW.at, '-58 seconds')
    AND c.session_id IN (
      SELECT NEW.session_id
      UNION SELECT id FROM task_sessions WHERE parent_session_id = NEW.session_id
      UNION SELECT id FROM task_sessions WHERE parent_session_id IN (SELECT id FROM task_sessions WHERE parent_session_id = NEW.session_id)
      UNION SELECT id FROM task_sessions WHERE parent_session_id IN (SELECT id FROM task_sessions WHERE parent_session_id IN (SELECT id FROM task_sessions WHERE parent_session_id = NEW.session_id)));
  DELETE FROM pend WHERE session_id = NEW.session_id;
END;`

func runCapMulti(t *testing.T, seed int64, done map[string]int) {
	const handles, rounds, perRound = 3, 14, 14
	stores := processes(t, handles)
	clocks := make([]*offsetClock, handles)
	for i, s := range stores {
		clocks[i] = &offsetClock{}
		s.now = clocks[i].now
	}
	db := stores[0].db
	root := seedBudget(t, db, 40)
	a := seedBudget(t, db, 10)
	b := seedBudget(t, db, 8)
	g := seedBudget(t, db, 4)
	for child, parent := range map[int64]int64{a: root, b: root, g: a} {
		if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, parent, child); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE task_sessions SET state = 'running'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(capMultiTriggers); err != nil {
		t.Fatal(err)
	}
	all := []int64{root, a, b, g}

	var mu sync.Mutex
	logs := make([][]string, handles)
	count := func(k string) { mu.Lock(); done[k]++; mu.Unlock() }
	var fails []string
	bad := func(h int, format string, a ...any) {
		mu.Lock()
		fails = append(fails, fmt.Sprintf("handle %d: %s", h, fmt.Sprintf(format, a...)))
		mu.Unlock()
	}
	worker := func(h int, rng *rand.Rand) {
		s := stores[h]
		owner := fmt.Sprintf("h%d", h)
		var runs, resv []int64
		n := 0
		ok := func(err error, extra ...error) bool { // errors the rules allow
			if err == nil || errors.Is(err, ErrAccountingMode) || errors.Is(err, ErrLimit) || errors.Is(err, ErrReservationLost) {
				return true
			}
			for _, e := range extra {
				if errors.Is(err, e) {
					return true
				}
			}
			return false
		}
		for i := 0; i < perRound; i++ {
			session := all[rng.Intn(len(all))]
			var what string
			var err error
			switch x := rng.Intn(100); {
			case x < 16:
				var r CapRun
				r, err = s.BeginCapRun(bg, "default", session, owner, fmt.Sprintf("run%d-%d", h, n))
				what = fmt.Sprintf("begin cap run on #%d", session)
				switch {
				case err == nil:
					runs = append(runs, r.ID)
					count("begin")
				case errors.Is(err, ErrAccountingMode):
					count("begin-refused")
				}
			case x < 32:
				if len(runs) == 0 {
					continue
				}
				id := runs[rng.Intn(len(runs))]
				err = s.HeartbeatCapRun(bg, "default", id, owner)
				what = fmt.Sprintf("heartbeat cap run #%d", id)
				if errors.Is(err, ErrReservationLost) {
					count("heartbeat-lost")
				}
			case x < 38:
				if len(runs) == 0 {
					continue
				}
				k := rng.Intn(len(runs))
				err = s.EndCapRun(bg, "default", runs[k], owner)
				what = fmt.Sprintf("end cap run #%d", runs[k])
				runs = append(runs[:k], runs[k+1:]...)
				if err != nil {
					bad(h, "%s: %v", what, err)
				}
			case x < 58:
				n++
				var r Reservation
				r, err = s.ReserveExecAsk(bg, "default", session, owner, "w", fmt.Sprintf("e%d-%d", h, n), float64(1+rng.Intn(3)), 0)
				what = fmt.Sprintf("reserve on #%d", session)
				switch {
				case err == nil:
					resv = append(resv, r.ID)
					count("reserve")
				case errors.Is(err, ErrAccountingMode):
					count("reserve-refused")
				}
			case x < 64:
				if len(resv) == 0 {
					continue
				}
				k := rng.Intn(len(resv))
				err = s.ReleaseReservation(bg, "default", resv[k], owner)
				what = fmt.Sprintf("release reservation #%d", resv[k])
				resv = append(resv[:k], resv[k+1:]...)
			case x < 70:
				_, err = s.ExpireStale(bg, "default", 0)
				what = "expire stale"
			case x < 88:
				v := []float64{0, 1, 2, 3, 4, 5, 6, 8, 10, 12, 20, 30, 40}[rng.Intn(13)]
				_, err = setBudget(s, session, v)
				what = fmt.Sprintf("set budget of #%d to $%.0f", session, v)
				switch {
				case err == nil:
					count("set")
				case errors.Is(err, ErrAccountingMode):
					count("lower-refused")
				}
			default:
				if len(resv) == 0 {
					continue
				}
				id := resv[rng.Intn(len(resv))]
				var lease string
				if e := s.db.QueryRow(`SELECT lease_until FROM task_session_reservations WHERE id = ?`, id).Scan(&lease); e == nil {
					if l, _ := time.Parse(timeFmt, lease); l.Before(clocks[h].now().Add(-ReservationGrace)) {
						count("heartbeat-lapsed")
					}
				}
				err = s.Heartbeat(bg, "default", id, owner)
				what = fmt.Sprintf("heartbeat reservation #%d", id)
			}
			mu.Lock()
			logs[h] = append(logs[h], fmt.Sprintf("%s -> err=%v", what, err))
			mu.Unlock()
			if !ok(err) {
				bad(h, "%s: unexpected error: %v", what, err)
			}
		}
	}
	rngs := make([]*rand.Rand, handles)
	for h := range rngs {
		rngs[h] = rand.New(rand.NewSource(seed*100 + int64(h)))
	}
	mover := rand.New(rand.NewSource(seed))
	for round := 0; round < rounds; round++ {
		for h := range clocks { // one handle's clock runs ahead, by up to a lease and a grace
			if mover.Intn(3) == 0 {
				clocks[h].shift.Add(int64([]time.Duration{30 * time.Second, 100 * time.Second, 200 * time.Second}[mover.Intn(3)]))
			}
		}
		var wg sync.WaitGroup
		for h := 0; h < handles; h++ {
			wg.Add(1)
			go func() { defer wg.Done(); worker(h, rngs[h]) }()
		}
		wg.Wait()
	}

	report := func(format string, a ...any) {
		t.Helper()
		var sb strings.Builder
		for h, l := range logs {
			fmt.Fprintf(&sb, "handle %d (shift %v):\n  %s\n", h, time.Duration(clocks[h].shift.Load()), strings.Join(l, "\n  "))
		}
		t.Fatalf("seed %d: %s\n%s", seed, fmt.Sprintf(format, a...), sb.String())
	}
	if len(fails) > 0 {
		report("%s", strings.Join(fails, "\n"))
	}
	rows, err := db.Query(`SELECT seq, what FROM viol ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []string
	for rows.Next() {
		var seq int
		var what string
		_ = rows.Scan(&seq, &what)
		verdicts = append(verdicts, fmt.Sprintf("#%d %s", seq, what))
	}
	rows.Close()
	if len(verdicts) > 0 {
		report("the writers broke the rules (in their own clocks):\n  %s", strings.Join(verdicts, "\n  "))
	}
	for _, id := range all {
		var bud float64
		if err := db.QueryRow(`SELECT budget_usd FROM task_sessions WHERE id = ?`, id).Scan(&bud); err != nil {
			t.Fatal(err)
		}
		if c := rcommit(t, stores[0], id); bud > 0 && c.Floor() > bud+1e-9 {
			report("session #%d commits $%.4f over its budget $%.2f (%+v)", id, c.Floor(), bud, c)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM task_session_cap_runs WHERE session_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > MaxCapRunsPerSession {
			report("session #%d has %d cap runs", id, n)
		}
	}
	var dup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM task_session_reservations GROUP BY session_id, worker_ref, exec_id HAVING COUNT(*) > 1)`).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	if dup > 0 {
		report("%d exec ids were granted twice", dup)
	}
}
