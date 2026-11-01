package dynorg

import (
	"fmt"
	"strings"
	"testing"
)

// More open directions than the operator's K (or than six) must still be re-evaluable: the cap on K bounds what is
// kept, not how many open directions the lead may list.
func TestReevaluateAcceptsMoreThanSixOpenDirections(t *testing.T) {
	r, c := evalRig(t, 3)
	var current []map[string]any
	for i := 0; i < HardMaxDirections+1; i++ {
		d := r.direction(fmt.Sprintf("dir %d", i))
		current = append(current, cur(d.Title, d.ID))
	}
	v, err := r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": "seven are open", "current": current}, candidatesJSON(8)))
	if err != nil {
		t.Fatalf("seven open directions: %v", err)
	}
	counts := map[string]int{}
	for _, d := range v.Decisions {
		counts[d.Action]++
	}
	if counts[ActKeep] != 3 || counts[ActDrop] != 4 || counts[ActAdd] != 0 {
		t.Fatalf("decisions %v: want 3 kept (K) and 4 dropped", counts)
	}
	if len(r.sessionNotes()) != 1 {
		t.Fatal("the re-evaluation was not recorded")
	}
}

func TestReevaluateStillBoundsTheCurrentList(t *testing.T) {
	r, c := evalRig(t, 3)
	_, err := r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": "x", "current": manyCurrent(MaxCandidates + 1)}, candidatesJSON(MaxCandidates)))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("an oversize current list: %v", err)
	}
}

// Two open directions whose titles normalize alike must not block a re-evaluation: they are matched by session id.
// They share one candidate; the first (lowest id) is that candidate's direction, the other is a merge into it when
// the candidate is kept, and a drop when it is not.
func TestReevaluateWithDirectionsOfTheSameNormalizedTitle(t *testing.T) {
	r, c := evalRig(t, 3)
	first, second := r.direction("Dir 0"), r.direction("  dir   0 ")
	other := r.direction("dir 1")
	current := []map[string]any{cur(first.Title, first.ID), cur(second.Title, second.ID), cur(other.Title, other.ID)}
	v, err := r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": "x", "current": current}, candidatesJSON(2)))
	if err != nil {
		t.Fatalf("same normalized title: %v", err)
	}
	got := map[int64]Decision{}
	for _, d := range v.Decisions {
		got[d.SessionID] = d
	}
	if got[first.ID].Action != ActKeep || got[other.ID].Action != ActKeep {
		t.Fatalf("decisions %+v", v.Decisions)
	}
	if d := got[second.ID]; d.Action != ActMerge || d.IntoSessionID != first.ID {
		t.Fatalf("the later duplicate: %+v", d)
	}
	// a swapped id is still refused
	if _, err := r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": "x", "current": []map[string]any{cur("dir 1", first.ID), cur(second.Title, second.ID), cur(other.Title, other.ID)}}, candidatesJSON(2))); err == nil {
		t.Fatal("a swapped id was accepted")
	}
}

func TestReevaluateDropsEveryDirectionOfADroppedTitle(t *testing.T) {
	cs := []DirCandidate{cand("a", 9, 1, 1, 1), cand("b", 2, 1, 1, 1)}
	_, ds, err := Reevaluate(cs, DefaultWeights, 1, []CurrentDirection{{Title: "a", SessionID: 1}, {Title: "B", SessionID: 2}, {Title: "b ", SessionID: 3}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Decision{{Action: ActKeep, Title: "a", SessionID: 1}, {Action: ActDrop, Title: "b", SessionID: 2}, {Action: ActDrop, Title: "b", SessionID: 3}}
	if fmt.Sprint(ds) != fmt.Sprint(want) {
		t.Fatalf("%+v, want %+v", ds, want)
	}
	// the same session twice is still an error
	if _, _, err := Reevaluate(cs, DefaultWeights, 1, []CurrentDirection{{Title: "a", SessionID: 1}, {Title: "A", SessionID: 1}}); err == nil {
		t.Fatal("one session listed twice was accepted")
	}
}
