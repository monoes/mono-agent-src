package coderhost

import (
	"errors"
	"testing"
)

func small(n int) []ScopeTask {
	var out []ScopeTask
	for i := 1; i <= n; i++ {
		t := ScopeTask{ID: int64(i), Size: "s"}
		if i > 1 {
			t.DependsOn = []int64{int64(i - 1)} // a chain: one after the other
		}
		out = append(out, t)
	}
	return out
}

func TestAdviceBranches(t *testing.T) {
	big := FactsOf(append(small(3), ScopeTask{ID: 9, Size: "s", DependsOn: nil}))
	highRisk := FactsOf([]ScopeTask{{ID: 1, Size: "xs", Risk: "high", Writes: true}})
	cases := []struct {
		name    string
		facts   SpawnFacts
		reason  string
		refused bool
		want    string
	}{
		{"small scope, first call, a valid reason: refused once", FactsOf(small(2)), ReasonParallel, false, AdviceRefuse},
		{"small scope, repeated: accepted", FactsOf(small(2)), ReasonParallel, true, AdviceAccept},
		{"an empty scope is small", FactsOf(nil), ReasonLongRunning, false, AdviceRefuse},
		{"three small tasks in a row are small", FactsOf(small(3)), ReasonContext, false, AdviceRefuse},
		{"four tasks are not small", FactsOf(small(4)), ReasonParallel, false, AdviceAccept},
		{"a parallel branch is not small", big, ReasonParallel, false, AdviceAccept},
		{"a task above size s is not small", FactsOf([]ScopeTask{{ID: 1, Size: "m"}}), ReasonLongRunning, false, AdviceAccept},
		{"a task with no size counts as above s", FactsOf([]ScopeTask{{ID: 1}}), ReasonLongRunning, false, AdviceAccept},
		{"two independent read-only tasks are a parallel branch", FactsOf([]ScopeTask{{ID: 1, Size: "s"}, {ID: 2, Size: "s"}}), ReasonLongRunning, false, AdviceAccept},
		{"two writers at once are not small, whatever else is read", SpawnFacts{OpenTasks: 2, Writers: 2}, ReasonLongRunning, false, AdviceAccept},
		{"isolation for a high-risk task is accepted at once", highRisk, ReasonIsolation, false, AdviceAccept},
		{"isolation for a task that is not high risk is refused once", FactsOf([]ScopeTask{{ID: 1, Size: "s", Risk: "medium"}}), ReasonIsolation, false, AdviceRefuse},
		{"a high-risk task with another reason is refused once", highRisk, ReasonParallel, false, AdviceRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AdviseSpawn(tc.facts, tc.reason, tc.refused)
			if err != nil || got.Decision != tc.want {
				t.Fatalf("advice = %+v, %v; want %s", got, err, tc.want)
			}
			if tc.want == AdviceRefuse && got.Message != ErrSpawnAdvice.Error() {
				t.Fatalf("message = %q", got.Message)
			}
		})
	}
}

func TestAdviceNeedsAValidReason(t *testing.T) {
	for _, r := range []string{"", "because", "PARALLEL", "parallel "} {
		if _, err := AdviseSpawn(FactsOf(small(5)), r, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("reason %q: %v", r, err)
		}
	}
	for _, r := range SpawnReasons {
		if _, err := AdviseSpawn(FactsOf(small(5)), r, false); err != nil {
			t.Errorf("reason %q: %v", r, err)
		}
	}
}

func TestFactsOfReadsTheShapeOfTheScope(t *testing.T) {
	chain := FactsOf([]ScopeTask{{ID: 1, Size: "s", Writes: true}, {ID: 2, Size: "xs", Writes: true, DependsOn: []int64{1}}, {ID: 3, Size: "s", Writes: true, DependsOn: []int64{2}}})
	if chain.Parallel || chain.Writers != 1 || chain.Larger != 0 || !chain.Small() {
		t.Fatalf("a chain = %+v", chain)
	}
	// 3 depends on 1 only through 2: still no parallel branch
	if f := FactsOf([]ScopeTask{{ID: 1, Size: "s"}, {ID: 2, Size: "s", DependsOn: []int64{1}}, {ID: 3, Size: "s", DependsOn: []int64{2}}}); f.Parallel {
		t.Fatalf("a transitive dependency read as parallel: %+v", f)
	}
	two := FactsOf([]ScopeTask{{ID: 1, Size: "s", Writes: true}, {ID: 2, Size: "s", Writes: true}})
	if !two.Parallel || two.Writers != 2 || two.Small() {
		t.Fatalf("two independent writers = %+v", two)
	}
	if f := FactsOf([]ScopeTask{{ID: 1, Size: "l"}, {ID: 2, Size: "m"}, {ID: 3, Size: "xs"}}); f.Larger != 2 {
		t.Fatalf("sizes = %+v", f)
	}
	if f := FactsOf(make([]ScopeTask, 20)); f.Small() {
		t.Fatal("twenty tasks are small")
	}
}
