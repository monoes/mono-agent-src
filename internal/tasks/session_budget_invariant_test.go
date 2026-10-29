package tasks

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// The budget model, written once more here as the test's oracle: a session's budget caps its whole
// subtree. commit(P) = spend + reserved + what its children hold; an open child holds
// max(budget, commit), a closed one commit; left(P) = budget - commit(P).
type budgetWorld struct {
	by   map[int64]Session
	kids map[int64][]int64
}

func readWorld(t *testing.T, s *Store) budgetWorld {
	t.Helper()
	all, err := s.ListSessions(bg, "default", SessionFilter{IncludeArchived: true}, human)
	if err != nil {
		t.Fatal(err)
	}
	w := budgetWorld{by: map[int64]Session{}, kids: map[int64][]int64{}}
	for _, se := range all {
		w.by[se.ID] = se
		if se.ParentSessionID != 0 {
			w.kids[se.ParentSessionID] = append(w.kids[se.ParentSessionID], se.ID)
		}
	}
	return w
}

func (w budgetWorld) commit(id int64) float64 {
	se := w.by[id]
	c := se.SpentUSD + se.ReservedUSD
	for _, k := range w.kids[id] {
		c += w.hold(k)
	}
	return c
}

func (w budgetWorld) hold(id int64) float64 {
	se := w.by[id]
	c := w.commit(id)
	if se.State == SessionDraft || se.State == SessionRunning || se.State == SessionPaused {
		return max(se.BudgetUSD, c)
	}
	return c
}

// subtreeSpend is the spend and reservations of a session and everything below it, in any state.
func (w budgetWorld) subtreeSpend(id int64) float64 {
	se := w.by[id]
	n := se.SpentUSD + se.ReservedUSD
	for _, k := range w.kids[id] {
		n += w.subtreeSpend(k)
	}
	return n
}

func (w budgetWorld) ancestors(id int64) []int64 {
	var out []int64
	for p := w.by[id].ParentSessionID; p != 0; p = w.by[p].ParentSessionID {
		out = append(out, p)
	}
	return out
}

type reservation struct {
	session int64
	amount  float64
}

type budgetRun struct {
	t        *testing.T
	s        *Store
	rng      *rand.Rand
	log      []string
	over     map[int64]float64 // overshoot a settle recorded, for the session and its ancestors
	reserved []reservation
	seed     int
}

func (r *budgetRun) fail(format string, a ...any) {
	r.t.Helper()
	r.t.Fatalf("seed %d: %s\nops:\n  %s", r.seed, fmt.Sprintf(format, a...), strings.Join(r.log, "\n  "))
}

func (r *budgetRun) pick(w budgetWorld) (Session, bool) {
	ids := make([]int64, 0, len(w.by))
	for id := range w.by {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return Session{}, false
	}
	return w.by[ids[r.rng.Intn(len(ids))]], true
}

// check asserts the invariant for every capped session after op; worsening is true when the op may
// legitimately push a session's left below where it was (an overshooting settle).
func (r *budgetRun) check(op string, before budgetWorld, ok, overshoot bool) {
	r.t.Helper()
	after := readWorld(r.t, r.s)
	for id, se := range after.by {
		if se.BudgetUSD <= 0 {
			continue
		}
		c := after.commit(id)
		if c > se.BudgetUSD+r.over[id]+1e-6 {
			r.fail("after %s: session #%d commits $%.4f (spent %.4f, reserved %.4f) of a $%.2f budget, overshoot allowed %.4f", op, id, c, se.SpentUSD, se.ReservedUSD, se.BudgetUSD, r.over[id])
		}
		if sub := after.subtreeSpend(id); sub > se.BudgetUSD+r.over[id]+1e-6 {
			r.fail("after %s: the subtree of #%d spent or holds $%.4f of a $%.2f budget (a closed session's spend disappeared?)", op, id, sub, se.BudgetUSD)
		}
		if ok && !overshoot {
			bs, had := before.by[id]
			if left := se.BudgetUSD - c; had && bs.BudgetUSD > 0 && left < -1e-6 && left < bs.BudgetUSD-before.commit(id)-1e-6 {
				r.fail("after %s: left of #%d fell to %.4f from %.4f", op, id, left, bs.BudgetUSD-before.commit(id))
			}
		}
	}
}

func (r *budgetRun) step(i int) {
	w := readWorld(r.t, r.s)
	se, any := r.pick(w)
	if !any {
		return
	}
	agent := Actor{Kind: Agent, Name: "lead", Session: se.ID}
	var op string
	ok, overshoot := false, false
	switch n := r.rng.Intn(100); {
	case n < 22: // createChild
		in := SessionInput{Title: "k", ParentSessionID: se.ID}
		if r.rng.Intn(2) == 0 {
			in.BudgetUSD = float64(1+r.rng.Intn(80)) / 4
		}
		actor := human
		if r.rng.Intn(2) == 0 {
			actor = agent
		}
		op = fmt.Sprintf("create child of #%d budget %.2f as %v", se.ID, in.BudgetUSD, actor.Kind)
		kid, err := r.s.CreateSession(bg, "default", in, actor)
		ok = err == nil
		if ok {
			for k := 0; k < 3; k++ {
				mustTask(r.t, r.s, "default", kid.ID, fmt.Sprintf("t%d", k))
			}
			op += fmt.Sprintf(" -> #%d $%.2f", kid.ID, kid.BudgetUSD)
		}
	case n < 32: // approve
		op = fmt.Sprintf("approve #%d", se.ID)
		_, err := r.s.ApproveSession(bg, "default", se.ID, human)
		ok = err == nil
	case n < 52: // claim, then reserve
		want := []float64{0, 0.5, 1, 3, 100}[r.rng.Intn(5)]
		op = fmt.Sprintf("claim in #%d then reserve %.2f", se.ID, want)
		tk, err := r.s.SessionNext(bg, "default", se.ID, Actor{Kind: Agent, Name: fmt.Sprintf("w%d", i), Session: se.ID}, true, 0)
		if err == nil && tk != nil {
			g, err := r.s.ReserveSpend(bg, "default", se.ID, want)
			ok = err == nil
			if ok {
				op += fmt.Sprintf(" -> granted %.2f", g)
				if se.BudgetUSD > 0 && g > 0 {
					r.reserved = append(r.reserved, reservation{se.ID, g})
				}
			}
		}
	case n < 70: // settle
		if len(r.reserved) == 0 {
			return
		}
		k := r.rng.Intn(len(r.reserved))
		res := r.reserved[k]
		r.reserved = append(r.reserved[:k], r.reserved[k+1:]...)
		actual := []float64{0, res.amount / 3, res.amount, res.amount + 0.5 + float64(r.rng.Intn(8))}[r.rng.Intn(4)]
		op = fmt.Sprintf("settle #%d reserved %.2f actual %.2f", res.session, res.amount, actual)
		ok = r.s.SettleSpend(bg, "default", res.session, res.amount, actual, "work") == nil
		if ok && actual > res.amount {
			overshoot = true
			for _, id := range append([]int64{res.session}, w.ancestors(res.session)...) {
				r.over[id] += actual - res.amount
			}
		}
	case n < 74: // a crashed worker: its reservations are released
		op = fmt.Sprintf("release the reservations of #%d", se.ID)
		ok = r.s.ReleaseReserved(bg, "default", se.ID) == nil
		if ok {
			kept := r.reserved[:0]
			for _, x := range r.reserved {
				if x.session != se.ID {
					kept = append(kept, x)
				}
			}
			r.reserved = kept
		}
	case n < 84: // stop, close or archive
		switch r.rng.Intn(3) {
		case 0:
			op = fmt.Sprintf("stop #%d", se.ID)
			_, err := r.s.StopSession(bg, "default", se.ID, human)
			ok = err == nil
		case 1:
			op = fmt.Sprintf("close #%d", se.ID)
			_, err := r.s.CloseSession(bg, "default", se.ID, SessionDone, "x", human)
			ok = err == nil
		default:
			op = fmt.Sprintf("archive #%d", se.ID)
			_, err := r.s.ArchiveSession(bg, "default", se.ID, human)
			ok = err == nil
		}
	default: // updateChild: raise, lower, to 0
		if se.ParentSessionID == 0 {
			return
		}
		var b float64
		switch r.rng.Intn(3) {
		case 0:
			b = se.BudgetUSD + float64(1+r.rng.Intn(20))/4
		case 1:
			b = max(se.BudgetUSD-float64(1+r.rng.Intn(20))/4, 0.25)
		}
		op = fmt.Sprintf("update #%d budget %.2f -> %.2f", se.ID, se.BudgetUSD, b)
		_, err := r.s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &b}, human)
		ok = err == nil
	}
	r.log = append(r.log, fmt.Sprintf("%02d %s ok=%v", i, op, ok))
	r.check(op, w, ok, overshoot)
}

// Random trees (depth up to 4) and random operation sequences hold the model's invariant after every
// operation: a session's budget caps its subtree, whatever order carves, reservations, settles, closes,
// updates and crashes arrive in. Deterministic by seed.
func TestBudgetInvariantHoldsOverRandomTrees(t *testing.T) {
	const seeds, ops = 200, 60
	for seed := 0; seed < seeds; seed++ {
		s, _, _ := newTestStore(t)
		r := &budgetRun{t: t, s: s, rng: rand.New(rand.NewSource(int64(seed))), over: map[int64]float64{}, seed: seed}
		budget := float64(10 * (1 + r.rng.Intn(2)))
		root, err := s.CreateSession(bg, "default", SessionInput{Title: "root", BudgetUSD: budget}, human)
		if err != nil {
			t.Fatal(err)
		}
		for k := 0; k < 3; k++ {
			mustTask(t, s, "default", root.ID, fmt.Sprintf("t%d", k))
		}
		mustRun(t, s, "default", root)
		for i := 0; i < ops; i++ {
			r.step(i)
		}
	}
}
