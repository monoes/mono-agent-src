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
// of the seed's profile: an OPEN session (draft, running or paused, not archived) has an access level
// no higher, and a max_tasks and a max_depth no larger, than the lowest value of the same field over
// ALL its ancestors, closed, stopped and archived ones included (the chain is walked to the root, not
// one level up); and when some ancestor has a budget, the open session's budget is at most the smallest
// nonzero ancestor budget, and above 0 unless it is a draft (a draft may still hold 0, and approving it
// is then refused). As a second check on stored data, every session, open or not, archived or not, holds
// an access level, task cap, depth cap and nonzero budget within that same minimum. The minimum is
// computed by the test itself from the rows it reads back (floorOf), not by the code under test, and a
// create, update or approve that the test knows would pass it must be refused (checked at once, and
// again by the invariant). THE OPERATIONS are drawn at random from eight equal slots: lower (the operator
// updates one or two limits of a random session, to values at or below its own), raise (the same with any
// values, so often above its ancestors'), stop, archive (each tried on a random session; the store may
// refuse, for a session in the wrong state or with open sessions below it), create a child as the
// operator (below a random session, or at top level one time in ten, asking at random for access, caps
// and budget or leaving each unset), create a child as an agent bound to a random session (below that
// session, asking at most for a task cap), and approve (two slots; tried on a random session, which
// only a draft below another session and holding a task can pass). A created session gets one task if
// it takes one. Each seed starts from one top-level session with random limits and runs 60 operations
// on a profile of its own; there are 200 seeds (1 to 200); trees reach at most 4 levels (the store's
// HardMaxSessionDepth) and the test stops creating at 14 sessions, so stopped and archived links appear
// in the middle of chains. A failure prints the seed and the full operation log. The test also fails if
// any operation kind never succeeds, or no refusal of a raise, a create or an approve ever happens, in
// the 200 seeds. OUT OF SCOPE: concurrent or interleaved writers (each operation runs alone, to its end),
// spend and the sum of sibling budgets (not part of this PR: a budget is held session by session to the
// minimum, never summed), the GUI and the CLI wrappers (this runs on the store), and a hand-edited parent
// cycle or corrupt row (the tree here is built only through the store; the cycle fails closed, with a test
// of its own in session_ceiling_test.go).
func TestSessionTreeCeilingProperty(t *testing.T) {
	s, db, _ := newTestStore(t)
	done := map[string]int{}
	for seed := int64(1); seed <= 200; seed++ {
		profile := addProfile(t, db, fmt.Sprintf("seed%d", seed))
		runCeilingSeed(t, s, profile, seed, done)
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

func runCeilingSeed(t *testing.T, s *Store, profile string, seed int64, done map[string]int) {
	rng := rand.New(rand.NewSource(seed))
	var log []string
	var ids []int64
	fail := func(step int, format string, a ...any) {
		t.Helper()
		t.Fatalf("seed %d, after op %d: %s\nop log:\n  %s", seed, step, fmt.Sprintf(format, a...), strings.Join(log, "\n  "))
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
			if err == nil {
				done["stop"]++
			}
		case 3:
			_, err := s.ArchiveSession(bg, profile, se.ID, human)
			log = append(log, fmt.Sprintf("archive #%d -> err=%v", se.ID, err))
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
			switch {
			case over && err == nil:
				fail(step, "create %+v was accepted above its ancestors' minimum %+v", in, f)
			case over:
				done["create-refused"]++
			case err == nil:
				ids = append(ids, got.ID)
				done[map[int]string{4: "create-operator", 5: "create-agent"}[kind]]++
				// a session may refuse the task (its cap); approving it is then refused for want of one
				_, _ = s.AddSessionTask(bg, profile, got.ID, SessionTaskInput{Title: "t", Acceptance: "done when t"}, human)
			}
		default: // approve
			f := floorOf(m, se.ParentSessionID)
			want := se.State == SessionDraft && !se.Archived && se.ParentSessionID != 0 && se.BudgetUSD <= 0 && f.budget > 0
			_, err := s.ApproveSession(bg, profile, se.ID, human)
			log = append(log, fmt.Sprintf("approve #%d (%s, $%.0f) -> err=%v", se.ID, se.State, se.BudgetUSD, err))
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
