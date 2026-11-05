package tasks

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A seeded model check of the two accounting modes of a session (reservations that hold money, cap runs that hold
// none) and of the set-budget rules that guard them. Each seed drives random operations over a small tree
// (root $40 -> a $10 -> g $4, root -> b $8, all running) and, after EVERY operation, checks:
//
//	P1 never a live cap run and a live reservation on the same session at once;
//	P2 a set-budget that lowers a budget (or sets one where there was none) is never accepted while a live cap run
//	   is on the session or below it; it is refused with ErrAccountingMode unless the ceiling above refuses it first;
//	   a raise, a restate and a removal are never refused with ErrAccountingMode;
//	P3 what is really committed (spent + held + what the children have spent or reserved, Commitment.Floor) never exceeds
//	   a budget (no operation overspends, none records a cost). The children's unspent allowances are not counted: a
//	   lowering clamps them without taking them back, so Commitment.Total may pass a budget after one (Q4, intended);
//	P4 a lapsed row counts for nothing: a reservation is refused (ErrAccountingMode) exactly while the model counts a
//	   live cap run on the session, a cap run exactly while it counts a live reservation, and the store's heartbeat of
//	   a cap run is lost exactly where the model says;
//	P5 a child's budget never exceeds the smallest nonzero budget above it (children are clamped by a lowering).
//
// "Live" is decided by the test itself from the rows it reads back and its own clock (lease >= now - ReservationGrace),
// not by the code under test. The clock moves in the random operations (20 s to 200 s), so leases lapse and revive.
// RESV_SEED replays one seed; a failure prints the seed and the full operation list. Seeds 1 to 40, 150 operations.
func TestCapRunPropertyInvariants(t *testing.T) {
	seeds := []int64{}
	if v := os.Getenv("RESV_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := int64(1); i <= 40; i++ {
			seeds = append(seeds, i)
		}
	}
	done := map[string]int{}
	for _, seed := range seeds {
		runCapModel(t, seed, 150, done)
	}
	if len(seeds) > 1 {
		for _, k := range []string{"begin", "begin-refused", "reserve", "reserve-refused", "lower-refused", "lower", "raise", "heartbeat-lost", "lapse-revive", "heartbeat-renewed", "heartbeat-lapsed-refused", "heartbeat-lapsed-renewed"} {
			if done[k] == 0 {
				t.Errorf("no %q ever happened in %d seeds: the test does not exercise it", k, len(seeds))
			}
		}
	}
}

type mcap struct {
	id      int64
	session int64
	owner   string
	lease   time.Time
}

func runCapModel(t *testing.T, seed int64, steps int, done map[string]int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	s, db, clk := newTestStore(t)
	var ops []string
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("seed %d: %s\noperations:\n  %s", seed, fmt.Sprintf(format, a...), strings.Join(ops, "\n  "))
	}
	root := rsession(t, s, db, 40)
	a := rkid(t, s, db, root, 10, "running")
	b := rkid(t, s, db, root, 8, "running")
	g := rkid(t, s, db, a, 4, "running")
	all := []int64{root, a, b, g}
	parent := map[int64]int64{a: root, b: root, g: a}
	below := func(id int64) []int64 { // the session and every session under it
		out := []int64{id}
		for _, k := range all {
			for p := parent[k]; p != 0; p = parent[p] {
				if p == id {
					out = append(out, k)
					break
				}
			}
		}
		return out
	}
	owners := []string{"p1", "p2", "p3"}
	var runs []*mcap
	live := func(lease time.Time) bool { return !lease.Before(clk.now().Add(-ReservationGrace)) }
	liveCaps := func(session int64) (n int) {
		for _, r := range runs {
			if r.session == session && live(r.lease) {
				n++
			}
		}
		return n
	}
	type resRow struct {
		id, session int64
		owner       string
		state       string
		lease       time.Time
	}
	readRes := func() (out []resRow) {
		rows, err := db.Query(`SELECT id, session_id, owner_id, state, lease_until FROM task_session_reservations ORDER BY id`)
		if err != nil {
			fail("reading the reservations: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var r resRow
			var lease string
			if err := rows.Scan(&r.id, &r.session, &r.owner, &r.state, &lease); err != nil {
				fail("%v", err)
			}
			r.lease, _ = time.Parse(timeFmt, lease)
			out = append(out, r)
		}
		return out
	}
	liveRes := func(session int64) (n int) {
		for _, r := range readRes() {
			if r.session == session && r.state == ReservationLive && live(r.lease) {
				n++
			}
		}
		return n
	}
	budgets := func() map[int64]float64 {
		m := map[int64]float64{}
		for _, id := range all {
			m[id] = budgetNow(t, s, id)
		}
		return m
	}
	capBelow := func(id int64) bool {
		for _, k := range below(id) {
			if liveCaps(k) > 0 {
				return true
			}
		}
		return false
	}
	pickRun := func() *mcap {
		if len(runs) == 0 {
			return nil
		}
		return runs[rng.Intn(len(runs))]
	}
	check := func(step string) {
		t.Helper()
		bs := budgets()
		for _, id := range all {
			if n, c := liveCaps(id), liveRes(id); n > 0 && c > 0 {
				fail("after %s: session #%d has %d live cap run(s) and %d live reservation(s) at once", step, id, n, c)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM task_session_cap_runs WHERE session_id = ? AND lease_until >= ?`, id, clk.now().Add(-ReservationGrace).UTC().Format(timeFmt)).Scan(&n); err != nil {
				fail("%v", err)
			}
			if n != liveCaps(id) {
				fail("after %s: session #%d has %d live cap run(s) in the store and %d in the model", step, id, n, liveCaps(id))
			}
			if bs[id] > 0 {
				if c := rcommit(t, s, id); c.Floor() > bs[id]+1e-9 {
					fail("after %s: session #%d commits $%.4f over its budget $%.2f (%+v)", step, id, c.Floor(), bs[id], c)
				}
			}
			for p := parent[id]; p != 0; p = parent[p] {
				if bs[p] > 0 && (bs[id] <= 0 || bs[id] > bs[p]+1e-9) {
					fail("after %s: session #%d has budget $%.2f against $%.2f above it", step, id, bs[id], bs[p])
				}
			}
		}
	}
	n := 0
	for step := 1; step <= steps; step++ {
		session := all[rng.Intn(len(all))]
		owner := owners[rng.Intn(len(owners))]
		x := rng.Intn(100)
		var desc string
		switch {
		case x < 12: // BeginCapRun
			wantRefused := liveRes(session) > 0
			r, err := s.BeginCapRun(bg, "default", session, owner, fmt.Sprintf("run%d", step))
			desc = fmt.Sprintf("begin cap run on #%d by %s -> err=%v", session, owner, err)
			ops = append(ops, desc)
			switch {
			case wantRefused && !errors.Is(err, ErrAccountingMode):
				fail("a cap run was begun on #%d beside a live reservation (err=%v)", session, err)
			case wantRefused:
				done["begin-refused"]++
			case err != nil:
				fail("a cap run was refused on #%d with no live reservation: %v", session, err)
			default:
				kept := runs[:0]
				for _, o := range runs { // the sweep of the dead rows of this session
					if o.session != session || live(o.lease) {
						kept = append(kept, o)
					}
				}
				runs = append(kept, &mcap{id: r.ID, session: session, owner: owner, lease: clk.now().Add(ReservationLease)})
				done["begin"]++
			}
		case x < 22: // HeartbeatCapRun
			r := pickRun()
			if r == nil {
				continue
			}
			who := r.owner
			if rng.Intn(8) == 0 {
				who = "stranger"
			}
			lapsed := !live(r.lease)
			lost := who != r.owner || lapsed && liveRes(r.session) > 0
			err := s.HeartbeatCapRun(bg, "default", r.id, who)
			desc = fmt.Sprintf("heartbeat cap run #%d (session #%d, owner %s, lapsed=%t) as %s -> err=%v", r.id, r.session, r.owner, lapsed, who, err)
			ops = append(ops, desc)
			switch {
			case lost && !errors.Is(err, ErrReservationLost):
				fail("the heartbeat of a run that must be lost succeeded or failed otherwise: %v", err)
			case !lost && err != nil:
				fail("the heartbeat of a run that may be renewed failed: %v", err)
			case lost:
				done["heartbeat-lost"]++
			default:
				if l := clk.now().Add(ReservationLease); l.After(r.lease) {
					r.lease = l
				}
				if lapsed {
					done["lapse-revive"]++
				}
			}
		case x < 30: // EndCapRun
			r := pickRun()
			if r == nil {
				continue
			}
			err := s.EndCapRun(bg, "default", r.id, r.owner)
			ops = append(ops, fmt.Sprintf("end cap run #%d -> err=%v", r.id, err))
			if err != nil {
				fail("ending a run failed: %v", err)
			}
			kept := runs[:0]
			for _, o := range runs {
				if o != r {
					kept = append(kept, o)
				}
			}
			runs = kept
		case x < 45: // ReserveExecAsk
			n++
			wantRefused := liveCaps(session) > 0
			want := float64(1 + rng.Intn(3))
			r, err := s.ReserveExecAsk(bg, "default", session, owner, "w", fmt.Sprintf("e%d", n), want, 0)
			desc = fmt.Sprintf("reserve $%.0f on #%d by %s -> %v, err=%v", want, session, owner, r.AmountUSD, err)
			ops = append(ops, desc)
			switch {
			case wantRefused && !errors.Is(err, ErrAccountingMode):
				fail("a reservation was granted on #%d beside a live cap run (err=%v)", session, err)
			case wantRefused:
				done["reserve-refused"]++
			case errors.Is(err, ErrAccountingMode):
				fail("a reservation was refused for a cap run on #%d, which has no live one", session)
			case err == nil:
				done["reserve"]++
			case !errors.Is(err, ErrLimit):
				fail("unexpected error: %v", err)
			}
		case x < 53: // ReleaseReservation
			rs := readRes()
			if len(rs) == 0 {
				continue
			}
			r := rs[rng.Intn(len(rs))]
			err := s.ReleaseReservation(bg, "default", r.id, r.owner)
			ops = append(ops, fmt.Sprintf("release reservation #%d (%s) -> err=%v", r.id, r.state, err))
			if err != nil && !errors.Is(err, ErrReservationLost) {
				fail("unexpected error: %v", err)
			}
		case x < 60: // Heartbeat of a reservation
			rs := readRes()
			if len(rs) == 0 {
				continue
			}
			r := rs[rng.Intn(len(rs))]
			lapsed := r.state == ReservationLive && !live(r.lease)
			lost := r.state != ReservationLive || lapsed && liveCaps(r.session) > 0
			want := clk.now().Add(ReservationLease) // the lease a renewal sets, or the longer one it already had
			if r.lease.After(want) {
				want = r.lease
			}
			err := s.Heartbeat(bg, "default", r.id, r.owner)
			ops = append(ops, fmt.Sprintf("heartbeat reservation #%d (%s, session #%d, lapsed=%t) -> err=%v", r.id, r.state, r.session, lapsed, err))
			after := readRes()
			switch {
			case lost && !errors.Is(err, ErrReservationLost):
				fail("the heartbeat of reservation #%d must be lost (state %s, lapsed=%t, live cap run=%t): %v", r.id, r.state, lapsed, liveCaps(r.session) > 0, err)
			case !lost && err != nil:
				fail("the heartbeat of reservation #%d may be renewed but failed: %v", r.id, err)
			}
			for _, a := range after {
				if a.id != r.id {
					continue
				}
				switch {
				case lost && !a.lease.Equal(r.lease):
					fail("the lost heartbeat of reservation #%d moved its lease from %v to %v", r.id, r.lease, a.lease)
				case !lost && a.lease.Before(want.Add(-time.Second)):
					fail("the heartbeat of reservation #%d did not renew its lease (%v -> %v, want %v)", r.id, r.lease, a.lease, want)
				}
			}
			switch {
			case lost && lapsed:
				done["heartbeat-lapsed-refused"]++
			case lapsed:
				done["heartbeat-lapsed-renewed"]++
			case !lost:
				done["heartbeat-renewed"]++
			}
		case x < 64: // ExpireStale
			sid := int64(0)
			if rng.Intn(2) == 0 {
				sid = session
			}
			c, err := s.ExpireStale(bg, "default", sid)
			ops = append(ops, fmt.Sprintf("expire stale on #%d -> %d, err=%v", sid, c, err))
			if err != nil {
				fail("%v", err)
			}
		case x < 82: // set-budget
			cur := budgets()[session]
			v := []float64{0, 1, 2, 3, 4, 5, 6, 8, 10, 12, 20, 30, 40, 50}[rng.Intn(14)]
			if rng.Intn(5) == 0 {
				v = cur // restate
			}
			lowers := v > 0 && v != cur && (cur <= 0 || v < cur)
			over := false // refused by the ceiling above, before the floor is asked
			for p := parent[session]; p != 0; p = parent[p] {
				if bp := budgetNow(t, s, p); bp > 0 && (v == 0 || v > bp) {
					over = true
				}
			}
			blocked := capBelow(session)
			_, err := setBudget(s, session, v)
			desc = fmt.Sprintf("set budget of #%d $%.0f -> $%.0f (lowers=%t, live cap run below=%t) -> err=%v", session, cur, v, lowers, blocked, err)
			ops = append(ops, desc)
			switch {
			case lowers && blocked && err == nil:
				fail("a budget was lowered on #%d while a live cap run is on its subtree", session)
			case lowers && blocked && !over && !errors.Is(err, ErrAccountingMode):
				fail("a lowering under a live cap run was refused for another reason: %v", err)
			case errors.Is(err, ErrAccountingMode) && !(lowers && blocked):
				fail("a set-budget that does not lower, or has no live cap run below it, was refused for the accounting mode: %v", err)
			case lowers && blocked:
				done["lower-refused"]++
			case err == nil && lowers:
				done["lower"]++
			case err == nil && v > cur:
				done["raise"]++
			case err != nil && !errors.Is(err, ErrLimit):
				fail("unexpected error: %v", err)
			}
		default: // the clock moves
			d := []time.Duration{20 * time.Second, 70 * time.Second, 130 * time.Second, 200 * time.Second}[rng.Intn(4)]
			clk.advance(d)
			ops = append(ops, fmt.Sprintf("advance the clock by %v", d))
		}
		check(fmt.Sprintf("step %d", step))
	}
}
