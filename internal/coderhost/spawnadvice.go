package coderhost

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The spawn advisory (docs/mastermind/plans/2026-10-28-mission-research-directions.md, section 3): a session is made
// only when it pays. A request for a small piece of work is refused once with "doable in this session"; the same
// request repeated with a reason that applies is accepted. Both functions here are pure; the store (sessionspawn.go)
// reads the facts from the tasks and remembers the refusal.

// Reasons a session may be spawned.
const (
	ReasonParallel        = "parallel"                  // independent work that can run at the same time
	ReasonLongRunning     = "long_running"              // work that would hold this session's turn for too long
	ReasonIsolation       = "isolation"                 // risky or destructive work that deserves its own writer, branch and verifier
	ReasonDifferentAccess = "different_access_or_model" // work that needs another access level, runtime or model
	ReasonContext         = "context_overflow"          // work that would not fit in this session's context
)

// SpawnReasons are the reasons the tools accept.
var SpawnReasons = []string{ReasonParallel, ReasonLongRunning, ReasonIsolation, ReasonDifferentAccess, ReasonContext}

// Small-scope thresholds: at most this many open tasks, each of size s or smaller.
const smallScopeTasks = 3

// ScopeTask is what the advisory knows about one open task of the requested scope, read from the task and its plan
// meta line, never from the request.
type ScopeTask struct {
	ID        int64
	Size      string // xs, s, m, l, or "" when the task has none (an operator's): unknown counts as larger than s
	Risk      string // low, medium, high or ""
	Writes    bool   // the task names files it touches
	DependsOn []int64
}

// SpawnFacts are the facts about the requested scope.
type SpawnFacts struct {
	OpenTasks int
	Larger    int  // tasks of a size above s, or of no size
	Writers   int  // writing tasks that could run at the same time
	Parallel  bool // two tasks of the scope with no dependency between them
	HighRisk  bool // the plan marks a task high risk
}

// FactsOf reads the facts of a scope. A dependency counts when it leads, directly or through other tasks of the
// scope, from one task to the other.
func FactsOf(ts []ScopeTask) SpawnFacts {
	f := SpawnFacts{OpenTasks: len(ts)}
	byID := map[int64]ScopeTask{}
	for _, t := range ts {
		byID[t.ID] = t
		if t.Size != "xs" && t.Size != "s" {
			f.Larger++
		}
		if t.Risk == "high" {
			f.HighRisk = true
		}
	}
	if len(ts) > 12 { // far past a small scope: the width does not matter
		f.Parallel, f.Writers = true, len(ts)
		return f
	}
	independent := func(a, b ScopeTask) bool { return !dependsOn(byID, a.ID, b.ID) && !dependsOn(byID, b.ID, a.ID) }
	var writers []ScopeTask
	for i, a := range ts {
		if a.Writes {
			writers = append(writers, a)
		}
		for _, b := range ts[i+1:] {
			if independent(a, b) {
				f.Parallel = true
			}
		}
	}
	if len(writers) > 0 {
		f.Writers = 1
		for i, a := range writers { // the widest set of writers no two of which depend on each other, for a few tasks
			w := 1
			for _, b := range writers[i+1:] {
				if independent(a, b) {
					w++
				}
			}
			f.Writers = max(f.Writers, w)
		}
	}
	return f
}

// dependsOn says whether task from depends on task to, directly or through other tasks of the scope.
func dependsOn(byID map[int64]ScopeTask, from, to int64) bool {
	seen := map[int64]bool{}
	stack := []int64{from}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range byID[id].DependsOn {
			if d == to {
				return true
			}
			if _, in := byID[d]; in && !seen[d] {
				seen[d] = true
				stack = append(stack, d)
			}
		}
	}
	return false
}

// Small says whether the scope can be done in one session: at most three open tasks, none above size s, one writer,
// and no parallel branch.
func (f SpawnFacts) Small() bool {
	return f.OpenTasks <= smallScopeTasks && f.Larger == 0 && f.Writers <= 1 && !f.Parallel
}

// Decision of the advisory.
const (
	AdviceAccept = "accepted"
	AdviceRefuse = "refused"
)

// Advice is the advisory's answer.
type Advice struct {
	Decision string
	Message  string // for a refusal: what to do
	Why      string // the branch taken, for the decisions ledger
}

// ErrSpawnAdvice is the refusal of the advisory: the work is doable in the spawner's own session. It is not a hard
// limit: repeating the request with a reason that applies is accepted.
var ErrSpawnAdvice = errors.New("doable in this session; run it here, or repeat with a reason that applies")

// AdviseSpawn decides. The reason must be one of SpawnReasons (an invalid one is the caller's error, checked first).
// alreadyRefused says an earlier call for the same request was refused by this advisory.
//
//   - a scope that is not small is accepted;
//   - a small scope with the reason isolation for a task the plan marks high risk is accepted;
//   - any other small scope is refused once, and accepted when the request is repeated.
func AdviseSpawn(f SpawnFacts, reason string, alreadyRefused bool) (Advice, error) {
	if !slices.Contains(SpawnReasons, reason) {
		return Advice{}, fmt.Errorf("%w: reason must be one of %s", ErrInvalid, strings.Join(SpawnReasons, ", "))
	}
	switch {
	case !f.Small():
		return Advice{Decision: AdviceAccept, Why: "the scope is not small"}, nil
	case reason == ReasonIsolation && f.HighRisk:
		return Advice{Decision: AdviceAccept, Why: "a small scope, but isolation for a task the plan marks high risk"}, nil
	case alreadyRefused:
		return Advice{Decision: AdviceAccept, Why: "a small scope, repeated with a reason after the one refusal"}, nil
	}
	return Advice{Decision: AdviceRefuse, Why: "a small scope: refused once",
		Message: ErrSpawnAdvice.Error()}, nil
}
