package tasks

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// TestSessionTreeCeilingProperty is a seeded model check of one rule: a session never exceeds the
// minimum of its whole ancestor chain. THE INVARIANT, checked after every operation on every session
// the seed has created: a session that has an ancestor holds an access level no higher, and a max_tasks
// and a max_depth no larger, than the lowest value of the same field over ALL its ancestors, closed,
// stopped and archived ones included (the chain is walked to the top, not one level up), and, when some
// ancestor has a budget, a budget at most the smallest nonzero ancestor budget. This applies to every such
// session, open (draft, running or paused) or not, archived or not; the top-level sessions are not checked.
// One further rule is limited to open sessions that are not drafts: under an ancestor with a budget they
// hold a budget above 0 (a draft may still hold 0, and approving it is then refused). The minimum is
// computed by the test itself from the rows it reads back (floorOf, which walks the parents from the
// session's parent up to the top, at most 20 steps), not by the code under test. A create or an update
// that asks for more than that minimum on some field (an update may also ask for budget 0 under an ancestor
// budget) must be refused (any error counts), and so must an
// approve of a draft that has no budget of its own under an ancestor budget; each is checked at once, and
// the invariant runs again after it. An approve is not checked against the other limits: a draft built
// through the store is always within them (the approve-time check on a hand-edited row is tested in
// session_approve_ceiling_test.go). THE OPERATIONS are drawn at random from eight equal slots: lower (the
// operator updates one or two limits of a random session, to values at or below its own, except that a
// session with no budget may be given any budget), raise (the same with any values, so often above its
// ancestors'), stop, archive (each tried on a random session; the store may refuse, for a session in the
// wrong state or with open sessions below it), create a child as the operator (below a random session, or
// at top level one time in ten, asking at random for access, caps and budget or leaving each unset),
// create a child as an agent bound to a random session (below that session, asking at most for a task
// cap), and approve (two slots; tried on a random session, which only a draft below another session and
// holding a task can pass). A created session gets one task if it takes one. Each seed starts from one
// top-level session with random limits and runs 60 operations on a profile of its own; there are 200
// seeds (1 to 200); trees reach at most 4 levels (the store's HardMaxSessionDepth) and the test stops
// creating at 14 sessions, so stopped and archived links appear in the middle of chains. A failure
// prints the seed and the full operation log. The test also fails if any operation kind never succeeds,
// or no refused update above the minimum, no refused create above it and no refused approve for want of a
// budget ever happens, in the 200 seeds. OUT OF SCOPE: concurrent or interleaved writers (each operation
// runs alone, to its end), spend and the sum of sibling budgets (not part of this PR: a budget is held
// session by session to the minimum, never summed), the GUI and the CLI wrappers (this runs on the
// store), and a hand-edited parent cycle or corrupt row (the tree here is built only through the store;
// the cycle fails closed, with a test of its own in session_ceiling_test.go).
func TestSessionTreeCeilingProperty(t *testing.T) {
	s, db, _ := newTestStore(t)
	done := map[string]int{}
	for seed := int64(1); seed <= 200; seed++ {
		profile := addProfile(t, db, fmt.Sprintf("seed%d", seed))
		runCeilingSeed(t, s, profile, seed, done, false)
	}
	for _, op := range []string{"lower", "raise", "stop", "archive", "create-operator", "create-agent", "approve"} {
		if done[op] == 0 {
			t.Errorf("no %s ever succeeded in 200 seeds: the test does not exercise it", op)
		}
	}
	for _, op := range []string{"raise-refused", "create-refused", "approve-refused"} {
		if done[op] == 0 {
			t.Errorf("no %s ever happened in 200 seeds: the test does not exercise it", op)
		}
	}
}

var (
	ceilLevels  = AccessLevels
	ceilTasks   = []int{1, 5, 20, 100, 200}
	ceilDepths  = []int{1, 2, 3, 4}
	ceilBudgets = []float64{0, 1, 3, 5, 8, 10}
)

// ceilingFloor is the lowest value of each limit over a chain, computed from rows read back.
type ceilingFloor struct {
	level, tasks, depth int
	budget              float64 // 0: no ancestor has a budget
}

func floorOf(byID map[int64]Session, parent int64) ceilingFloor {
	f := ceilingFloor{level: len(ceilLevels), tasks: 1 << 30, depth: 1 << 30}
	for seen := 0; parent != 0 && seen < 20; seen++ {
		p, ok := byID[parent]
		if !ok {
			break
		}
		f.level = min(f.level, slices.Index(ceilLevels, p.AccessLevel))
		f.tasks = min(f.tasks, p.MaxTasks)
		f.depth = min(f.depth, p.MaxDepth)
		if p.BudgetUSD > 0 && (f.budget == 0 || p.BudgetUSD < f.budget) {
			f.budget = p.BudgetUSD
		}
		parent = p.ParentSessionID
	}
	return f
}

func runCeilingSeed(t *testing.T, s *Store, profile string, seed int64, done map[string]int, strict bool) {
	rng := rand.New(rand.NewSource(seed))
	var log []string
	var ids []int64
	hasTask := map[int64]bool{} // sessions on which AddSessionTask succeeded (the model's own record)
	fail := func(step int, format string, a ...any) {
		t.Helper()
		t.Fatalf("seed %d, after op %d: %s\nop log:\n  %s", seed, step, fmt.Sprintf(format, a...), strings.Join(log, "\n  "))
	}
	// exact, in strict mode, holds the store to the model's verdict on an operation: a legal one is
	// accepted and an illegal one refused.
	exact := func(step int, kind, op string, legal bool, err error) {
		switch {
		case !strict:
		case legal && err != nil:
			fail(step, "%s was refused but is legal by the model: %v", op, err)
		case !legal && err == nil:
			fail(step, "%s was accepted but is illegal by the model", op)
		case legal:
			done["legal-"+kind]++
		default:
			done["illegal-"+kind]++
		}
	}
	read := func() map[int64]Session {
		m := map[int64]Session{}
		for _, id := range ids {
			se, err := s.GetSession(bg, profile, id, human)
			if err != nil {
				t.Fatalf("seed %d: reading #%d: %v", seed, id, err)
			}
			m[id] = se
		}
		return m
	}
	pick := func(m map[int64]Session) Session { return m[ids[rng.Intn(len(ids))]] }
	rootLimits := SessionInput{Title: "root", AccessLevel: ceilLevels[rng.Intn(4)], MaxTasks: ceilTasks[rng.Intn(5)], MaxDepth: ceilDepths[rng.Intn(4)], BudgetUSD: ceilBudgets[rng.Intn(6)]}
	root, err := s.CreateSession(bg, profile, rootLimits, human)
	if err != nil {
		t.Fatalf("seed %d: %v", seed, err)
	}
	ids = append(ids, root.ID)
	log = append(log, fmt.Sprintf("create top-level %+v -> #%d", rootLimits, root.ID))

	for step := 1; step <= 60; step++ {
		m := read()
		se := pick(m)
		kind := rng.Intn(8)
		switch kind {
		case 0, 1: // lower (0) or raise (1)
			u := SessionUpdate{}
			for n := 1 + rng.Intn(2); n > 0; n-- {
				switch rng.Intn(4) {
				case 0:
					v := ceilLevels[rng.Intn(4)]
					if kind == 0 {
						v = ceilLevels[rng.Intn(slices.Index(ceilLevels, se.AccessLevel)+1)]
					}
					u.AccessLevel = &v
				case 1:
					v := ceilTasks[rng.Intn(5)]
					if kind == 0 {
						v = min(v, se.MaxTasks)
					}
					u.MaxTasks = &v
				case 2:
					v := ceilDepths[rng.Intn(4)]
					if kind == 0 {
						v = min(v, se.MaxDepth)
					}
					u.MaxDepth = &v
				default:
					v := ceilBudgets[rng.Intn(6)]
					if kind == 0 && se.BudgetUSD > 0 {
						v = ceilBudgets[1+rng.Intn(5)]
						v = min(v, se.BudgetUSD)
					}
					u.BudgetUSD = &v
				}
			}
			name := map[int]string{0: "lower", 1: "raise"}[kind]
			f := floorOf(m, se.ParentSessionID)
			over := se.ParentSessionID != 0 && updateOver(u, f)
			_, err := s.UpdateSession(bg, profile, se.ID, u, human)
			log = append(log, fmt.Sprintf("%s #%d %s -> err=%v", name, se.ID, showUpdate(u), err))
			exact(step, "update", name+" #"+fmt.Sprint(se.ID), !over, err)
			switch {
			case over && err == nil:
				fail(step, "update of #%d %s was accepted above its ancestors' minimum %+v", se.ID, showUpdate(u), f)
			case over:
				done["raise-refused"]++
			case err == nil:
				done[name]++
			}
		case 2:
			_, err := s.StopSession(bg, profile, se.ID, human)
			log = append(log, fmt.Sprintf("stop #%d -> err=%v", se.ID, err))
			exact(step, "stop", fmt.Sprint("stop #", se.ID), isOpen(se.State) && !se.Archived, err)
			if err == nil {
				done["stop"]++
			}
		case 3:
			_, err := s.ArchiveSession(bg, profile, se.ID, human)
			log = append(log, fmt.Sprintf("archive #%d -> err=%v", se.ID, err))
			exact(step, "archive", fmt.Sprint("archive #", se.ID), archivable(m, se), err)
			if err == nil {
				done["archive"]++
			}
		case 4, 5: // create below a session as the operator (4) or as a bound agent (5)
			if len(ids) >= 14 {
				continue
			}
			in := SessionInput{Title: "k", ParentSessionID: se.ID, Kind: ""}
			actor := human
			if kind == 4 {
				if rng.Intn(10) == 0 {
					in.ParentSessionID = 0
				}
				if rng.Intn(2) == 0 {
					in.AccessLevel = ceilLevels[rng.Intn(4)]
				}
				if rng.Intn(2) == 0 {
					in.MaxTasks = ceilTasks[rng.Intn(5)]
				}
				if rng.Intn(2) == 0 {
					in.MaxDepth = ceilDepths[rng.Intn(4)]
				}
				if rng.Intn(2) == 0 {
					in.BudgetUSD = ceilBudgets[rng.Intn(6)]
				}
			} else {
				actor = Actor{Kind: Agent, Name: "lead", Session: se.ID}
				if rng.Intn(2) == 0 {
					in.MaxTasks = ceilTasks[rng.Intn(5)]
				}
			}
			f := floorOf(m, in.ParentSessionID)
			over := in.ParentSessionID != 0 && createOver(in, f)
			got, err := s.CreateSession(bg, profile, in, actor)
			log = append(log, fmt.Sprintf("create (%v) %+v -> err=%v", actor.Kind, in, err))
			exact(step, "create", fmt.Sprintf("create (%v) %+v", actor.Kind, in), !over && mayCreateBelow(m, in.ParentSessionID, actor), err)
			switch {
			case over && err == nil:
				fail(step, "create %+v was accepted above its ancestors' minimum %+v", in, f)
			case over:
				done["create-refused"]++
			case err == nil:
				ids = append(ids, got.ID)
				done[map[int]string{4: "create-operator", 5: "create-agent"}[kind]]++
				// a session may refuse the task (its cap); approving it is then refused for want of one
				if _, err := s.AddSessionTask(bg, profile, got.ID, SessionTaskInput{Title: "t", Acceptance: "done when t"}, human); err == nil {
					hasTask[got.ID] = true
				}
			}
		default: // approve
			f := floorOf(m, se.ParentSessionID)
			want := se.State == SessionDraft && !se.Archived && se.ParentSessionID != 0 && se.BudgetUSD <= 0 && f.budget > 0
			_, err := s.ApproveSession(bg, profile, se.ID, human)
			log = append(log, fmt.Sprintf("approve #%d (%s, $%.0f) -> err=%v", se.ID, se.State, se.BudgetUSD, err))
			legal := se.State == SessionDraft && !se.Archived && hasTask[se.ID] &&
				(se.ParentSessionID == 0 || !storedOver(se, f) && (se.BudgetUSD > 0 || f.budget == 0))
			exact(step, "approve", fmt.Sprint("approve #", se.ID), legal, err)
			switch {
			case want && err == nil:
				fail(step, "approve of #%d with no budget was accepted under an ancestor budget of $%.0f", se.ID, f.budget)
			case want:
				done["approve-refused"]++
			case err == nil:
				done["approve"]++
			}
		}
		checkCeilings(t, read(), func(format string, a ...any) { fail(step, format, a...) })
	}
}

// archivable: a plain archive needs a session that is not archived, not running or paused, with no
// open (draft, running or paused, not archived) session anywhere below it.
func archivable(m map[int64]Session, se Session) bool {
	if se.Archived || se.State == SessionRunning || se.State == SessionPaused {
		return false
	}
	for _, k := range m {
		for p, n := k.ParentSessionID, 0; p != 0 && n < 20; n++ {
			if p == se.ID {
				if !k.Archived && isOpen(k.State) {
					return false
				}
				break
			}
			p = m[p].ParentSessionID
		}
	}
	return true
}

// mayCreateBelow: apart from the limits, a session is started below a parent that is open (not
// archived; draft, running or paused) and at level 3 or higher in the tree (the new one is at most
// level 4), and a bound agent adds at most MaxOpenChildren open children to one parent. A top-level
// session by the operator is always legal here.
func mayCreateBelow(m map[int64]Session, parent int64, actor Actor) bool {
	if parent == 0 {
		return actor.Kind == Human
	}
	par := m[parent]
	level := 0
	for p := parent; p != 0 && level < 20; p = m[p].ParentSessionID {
		level++
	}
	if par.Archived || !isOpen(par.State) || level >= HardMaxSessionDepth {
		return false
	}
	kids := 0
	for _, k := range m {
		if k.ParentSessionID == parent && !k.Archived && isOpen(k.State) {
			kids++
		}
	}
	return actor.Kind == Human || kids < MaxOpenChildren
}

// storedOver: a session's own stored limits are above a floor (the check an approval makes).
func storedOver(se Session, f ceilingFloor) bool {
	return slices.Index(ceilLevels, se.AccessLevel) > f.level || se.MaxTasks > f.tasks || se.MaxDepth > f.depth ||
		f.budget > 0 && se.BudgetUSD > f.budget
}

func updateOver(u SessionUpdate, f ceilingFloor) bool {
	return u.AccessLevel != nil && slices.Index(ceilLevels, *u.AccessLevel) > f.level ||
		u.MaxTasks != nil && *u.MaxTasks > f.tasks ||
		u.MaxDepth != nil && *u.MaxDepth > f.depth ||
		u.BudgetUSD != nil && f.budget > 0 && (*u.BudgetUSD == 0 || *u.BudgetUSD > f.budget)
}

func createOver(in SessionInput, f ceilingFloor) bool {
	level := in.AccessLevel
	if level == "" {
		level = DefaultAccessLevel
	}
	return slices.Index(ceilLevels, level) > f.level ||
		in.MaxTasks != 0 && in.MaxTasks > f.tasks ||
		in.MaxDepth != 0 && in.MaxDepth > f.depth ||
		f.budget > 0 && in.BudgetUSD > f.budget
}

func showUpdate(u SessionUpdate) string {
	var p []string
	if u.AccessLevel != nil {
		p = append(p, "access="+*u.AccessLevel)
	}
	if u.MaxTasks != nil {
		p = append(p, fmt.Sprint("tasks=", *u.MaxTasks))
	}
	if u.MaxDepth != nil {
		p = append(p, fmt.Sprint("depth=", *u.MaxDepth))
	}
	if u.BudgetUSD != nil {
		p = append(p, fmt.Sprintf("budget=%.0f", *u.BudgetUSD))
	}
	return strings.Join(p, ",")
}

func checkCeilings(t *testing.T, m map[int64]Session, fail func(string, ...any)) {
	t.Helper()
	for _, se := range m {
		f := floorOf(m, se.ParentSessionID)
		if se.ParentSessionID == 0 {
			continue
		}
		open := !se.Archived && isOpen(se.State)
		switch {
		case slices.Index(ceilLevels, se.AccessLevel) > f.level:
			fail("#%d (%s, open=%t) has access %s above its ancestors' minimum %s", se.ID, se.State, open, se.AccessLevel, ceilLevels[f.level])
		case se.MaxTasks > f.tasks:
			fail("#%d (%s, open=%t) has max_tasks %d above its ancestors' minimum %d", se.ID, se.State, open, se.MaxTasks, f.tasks)
		case se.MaxDepth > f.depth:
			fail("#%d (%s, open=%t) has max_depth %d above its ancestors' minimum %d", se.ID, se.State, open, se.MaxDepth, f.depth)
		case f.budget > 0 && se.BudgetUSD > f.budget:
			fail("#%d (%s, open=%t) has budget $%.0f above its ancestors' minimum $%.0f", se.ID, se.State, open, se.BudgetUSD, f.budget)
		case f.budget > 0 && open && se.State != SessionDraft && se.BudgetUSD <= 0:
			fail("#%d (%s) runs with no budget under an ancestor budget of $%.0f", se.ID, se.State, f.budget)
		}
	}
}

// TestSessionTreeLegalOpsAreAcceptedProperty is the other half of TestSessionTreeCeilingProperty, on the
// same generator and the same seeds (it runs runCeilingSeed in strict mode, so the diff is small and the
// two halves cannot drift apart). THE INVARIANT: an operation that the test's model decides is LEGAL is
// ACCEPTED by the store, and one the model decides is illegal is refused. The model decides BEFORE each
// operation, from the rows read back, with floorOf and its own state rules. LEGAL means: an update by the
// operator is legal when none of its fields is above the minimum over the whole ancestor chain (and a
// budget of 0 is not asked under an ancestor budget); a create is legal when it is within that minimum
// and the parent is open and not archived, is at level 3 or above, and, for a bound agent, has fewer than
// MaxOpenChildren open children (an operator's top-level create is always legal); a stop is legal on a
// draft, running or paused session that is not archived; an archive is legal on a session that is not
// archived, not running or paused, and has no open session below it; an approve is legal on a draft,
// not archived, that holds a task and, when it has a parent, whose stored limits are within the minimum and
// that has a budget unless no ancestor has one. Seeds 1 to 200, 60 operations each, the same eight slots.
// The test also fails if, in the 200 seeds, a legal and an illegal operation of each of these five kinds
// (update, create, stop, archive, approve) never both happen. OUT OF SCOPE: the operations the generator
// does not draw (rename, cascade archive, pause, resume, close, spend; none changes a limit or a parent), a
// bound agent's other refusals (a task token, another session), and concurrent writers.
func TestSessionTreeLegalOpsAreAcceptedProperty(t *testing.T) {
	s, db, _ := newTestStore(t)
	done := map[string]int{}
	for seed := int64(1); seed <= 200; seed++ {
		profile := addProfile(t, db, fmt.Sprintf("legal%d", seed))
		runCeilingSeed(t, s, profile, seed, done, true)
	}
	for _, kind := range []string{"update", "create", "stop", "archive", "approve"} {
		for _, side := range []string{"legal-", "illegal-"} {
			if done[side+kind] == 0 {
				t.Errorf("no %s%s operation happened in 200 seeds: the test does not exercise it", side, kind)
			}
		}
	}
}
