package tasks

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A seeded model check of the reservation store. Each run drives random operations over a small tree of sessions
// against a plain-Go model of the money, and after every step checks:
//
//	I1 for each budgeted session, spent + held + children's hold <= budget (+eps), unless a cost above its
//	   reservation was recorded on it (an overspend is recorded as it happened, never refused);
//	I3 an operation by a stranger changes nothing, so only the owner ends a live row;
//	I4 a crash (a dropped handle swept by ExpireStale) never lowers the total held;
//	and the store's commitment equals the model's, to the cent.
//
// I2 (concurrent reservers never get more than left) is the goroutine test. RESV_SEED replays one seed; a failure
// prints the seed and the full operation list.
func TestReservationPropertyInvariants(t *testing.T) {
	seeds := []int64{}
	if v := os.Getenv("RESV_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := int64(1); i <= 30; i++ {
			seeds = append(seeds, i)
		}
	}
	for _, seed := range seeds {
		runReservationModel(t, seed, 120)
	}
}

type mres struct {
	id             int64
	session        int64
	owner          string
	amount, charge float64
	state          string
}

func (m *mres) held() float64 {
	if m.state != ReservationLive && m.state != ReservationExpired {
		return 0
	}
	if h := m.amount - m.charge; h > 0 {
		return h
	}
	return 0
}

func runReservationModel(t *testing.T, seed int64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	s, db, clk := newTestStore(t)
	var ops []string
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("seed %d: %s\noperations:\n  %s", seed, fmt.Sprintf(format, a...), strings.Join(ops, "\n  "))
	}
	// tree: root(40) -> openKid(10), kid2(8), and openKid -> grand(4): a child spends only inside its own budget
	root := rsession(t, s, db, 40)
	openKid := rkid(t, s, db, root, 10, "running")
	kid2 := rkid(t, s, db, root, 8, "running")
	grand := rkid(t, s, db, openKid, 4, "running") // budgeted: an unbudgeted session under a budgeted one is refused elsewhere
	all := []int64{root, openKid, kid2, grand}
	budget := map[int64]float64{root: 40, openKid: 10, kid2: 8, grand: 4}
	spent := map[int64]float64{}
	overspent := map[int64]bool{}
	var rows []*mres
	owners := []string{"p1", "p2", "p3"}

	var hold func(id int64) (own, total float64)
	hold = func(id int64) (own, total float64) { // own: reservations held; total: spent + held + children's hold
		var kids []int64
		switch id {
		case root:
			kids = []int64{openKid, kid2}
		case openKid:
			kids = []int64{grand}
		}
		total = spent[id]
		for _, r := range rows {
			if r.session == id {
				own += r.held()
			}
		}
		total += own
		for _, k := range kids {
			_, kt := hold(k)
			if budget[k] > kt { // every session of the tree is open
				kt = budget[k]
			}
			total += kt
		}
		return own, total
	}
	heldAll := func() float64 {
		sum := 0.0
		for _, r := range rows {
			sum += r.held()
		}
		return sum
	}
	check := func() {
		t.Helper()
		for _, id := range all {
			c, err := s.Commitment(bg, "default", id)
			if err != nil {
				fail("commitment of #%d: %v", id, err)
			}
			own, total := hold(id)
			if !near(c.Held, own) || !near(c.Total(), total) || !near(c.Spent, spent[id]) {
				fail("session #%d: store %+v, model held %v total %v spent %v", id, c, own, total, spent[id])
			}
			if budget[id] > 0 && !overspent[id] && total > budget[id]+1e-9 {
				fail("I1: session #%d commits %v of a budget of %v", id, total, budget[id])
			}
		}
	}
	parent := map[int64]int64{openKid: root, kid2: root, grand: openKid}
	overspend := func(id int64) { // an overspend is recorded where it happened and counts against every ancestor
		for ; id != 0; id = parent[id] {
			overspent[id] = true
		}
	}
	pick := func() *mres {
		if len(rows) == 0 {
			return nil
		}
		return rows[rng.Intn(len(rows))]
	}
	snapshot := func(r *mres) Reservation { return rrow(t, s, r.id) }

	for step := 0; step < steps; step++ {
		var op string
		switch n := rng.Intn(100); {
		case n < 28: // reserve
			sid := all[rng.Intn(len(all))]
			owner := owners[rng.Intn(len(owners))]
			want := float64(rng.Intn(120)) / 10 // 0 = all that is left
			op = fmt.Sprintf("reserve session #%d owner %s want %.1f", sid, owner, want)
			ops = append(ops, op)
			r, err := s.Reserve(bg, "default", sid, owner, "w", want)
			if err != nil {
				if !errorsIsLimit(err) {
					fail("%s: %v", op, err)
				}
				continue
			}
			rows = append(rows, &mres{id: r.ID, session: sid, owner: owner, amount: r.AmountUSD, state: ReservationLive})
		case n < 40: // heartbeat by the owner or a stranger
			r := pick()
			if r == nil {
				continue
			}
			who := r.owner
			stranger := rng.Intn(3) == 0
			if stranger {
				who = "stranger"
			}
			op = fmt.Sprintf("heartbeat #%d by %s", r.id, who)
			ops = append(ops, op)
			before := snapshot(r)
			err := s.Heartbeat(bg, "default", r.id, who)
			if stranger {
				if err == nil || snapshot(r) != before {
					fail("I3: %s changed things (%v)", op, err)
				}
			} else if (r.state == ReservationLive) != (err == nil) {
				fail("%s: state %s, err %v", op, r.state, err)
			}
		case n < 58: // settle, by the owner or a stranger
			r := pick()
			if r == nil {
				continue
			}
			who := r.owner
			stranger := rng.Intn(4) == 0
			if stranger {
				who = "stranger"
			}
			actual := float64(rng.Intn(60)) / 10
			op = fmt.Sprintf("settle #%d by %s actual %.1f", r.id, who, actual)
			ops = append(ops, op)
			before, sp := snapshot(r), rspent(t, db, r.session)
			err := s.Settle(bg, "default", r.id, who, actual)
			if stranger || r.state != ReservationLive {
				if err == nil || snapshot(r) != before || rspent(t, db, r.session) != sp {
					fail("I3: %s changed things (%v)", op, err)
				}
				continue
			}
			if err != nil {
				fail("%s: %v", op, err)
			}
			if actual > r.amount+1e-9 {
				overspend(r.session)
			}
			spent[r.session] += actual
			r.charge, r.state = actual, ReservationSettled
		case n < 66: // release, by the owner or a stranger
			r := pick()
			if r == nil {
				continue
			}
			who := r.owner
			stranger := rng.Intn(4) == 0
			if stranger {
				who = "stranger"
			}
			op = fmt.Sprintf("release #%d by %s", r.id, who)
			ops = append(ops, op)
			before := snapshot(r)
			err := s.ReleaseReservation(bg, "default", r.id, who)
			if stranger || r.state != ReservationLive {
				if err == nil || snapshot(r) != before {
					fail("I3: %s changed things (%v)", op, err)
				}
				continue
			}
			if err != nil {
				fail("%s: %v", op, err)
			}
			r.state = ReservationReleased
		case n < 80: // crash: some owners stop heartbeating, time passes, a sweep runs
			dead := owners[rng.Intn(len(owners))]
			op = fmt.Sprintf("crash %s then sweep", dead)
			ops = append(ops, op)
			for _, r := range rows { // the survivors heartbeat, the dead one's handle is dropped
				if r.state == ReservationLive && r.owner != dead {
					if err := s.Heartbeat(bg, "default", r.id, r.owner); err != nil {
						fail("%s: survivor heartbeat: %v", op, err)
					}
				}
			}
			before := heldAll()
			clk.advance(ReservationLease + ReservationGrace + time.Second)
			for _, r := range rows { // survivors keep their handles fresh across the long step
				if r.state == ReservationLive && r.owner != dead {
					if err := s.Heartbeat(bg, "default", r.id, r.owner); err != nil {
						fail("%s: survivor heartbeat: %v", op, err)
					}
				}
			}
			if _, err := s.ExpireStale(bg, "default", 0); err != nil {
				fail("%s: %v", op, err)
			}
			for _, r := range rows {
				if r.state == ReservationLive && r.owner == dead {
					r.state = ReservationExpired
				}
			}
			if after := heldAll(); !near(after, before) {
				fail("I4: a crash moved the money held from %v to %v", before, after)
			}
			for _, r := range rows {
				if g := snapshot(r); g.State != r.state {
					fail("%s: #%d is %s in the store, %s in the model", op, r.id, g.State, r.state)
				}
			}
		case n < 90: // the operator resolves an expired row
			r := pick()
			if r == nil {
				continue
			}
			charge := float64(rng.Intn(80)) / 10
			op = fmt.Sprintf("resolve #%d charge %.1f", r.id, charge)
			ops = append(ops, op)
			err := s.ResolveReservation(bg, "default", r.id, charge, "note", human)
			if r.state != ReservationExpired {
				if err == nil {
					fail("%s: resolved a %s row", op, r.state)
				}
				continue
			}
			if err != nil {
				fail("%s: %v", op, err)
			}
			if charge > r.held()+1e-9 {
				overspend(r.session)
			}
			spent[r.session] += charge
			r.charge, r.state = charge, ReservationResolved
		default: // time passes without a sweep, owners heartbeat
			op = "tick"
			ops = append(ops, op)
			clk.advance(30 * time.Second)
			for _, r := range rows {
				if r.state == ReservationLive {
					if err := s.Heartbeat(bg, "default", r.id, r.owner); err != nil {
						fail("tick heartbeat #%d: %v", r.id, err)
					}
				}
			}
		}
		check()
	}
	t.Logf("seed %d: %d operations, %d reservations", seed, len(ops), len(rows))
}

func errorsIsLimit(err error) bool { return strings.Contains(err.Error(), ErrLimit.Error()) }
