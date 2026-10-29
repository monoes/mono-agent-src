package tasks

import (
	"errors"
	"testing"
)

// Review of PR #1141, P2: an update of an existing child is held to its parent like its creation.
func TestAnUpdateOfAChildStaysWithinItsParent(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, _ := carveParent(t, s, 10)
	k := kidOf(t, s, p.ID, 5)
	other := kidOf(t, s, p.ID, 3)
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	str := func(v string) *string { return &v }
	for name, u := range map[string]SessionUpdate{
		"a budget above the parent's":       {BudgetUSD: f(20)},
		"a budget past what a sibling left": {BudgetUSD: f(8)},
		"no cap under a capped parent":      {BudgetUSD: f(0)},
		"max tasks above the parent's":      {MaxTasks: i(p.MaxTasks + 1)},
		"max depth above the parent's":      {MaxDepth: i(p.MaxDepth + 1)},
	} {
		if _, err := s.UpdateSession(bg, "default", k.ID, u, human); !errors.Is(err, ErrLimit) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// access: the parent is lowered first, then the child may not be raised past it
	lowest := AccessLevels[0]
	if _, err := s.UpdateSession(bg, "default", p.ID, SessionUpdate{AccessLevel: str(lowest)}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{AccessLevel: str(AccessLevels[len(AccessLevels)-1])}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("access above the parent's: %v", err)
	}
	if got, _ := s.GetSession(bg, "default", k.ID, human); got.BudgetUSD != 5 || got.MaxTasks > p.MaxTasks {
		t.Fatalf("a refused update changed the child: %+v", got)
	}
	// $10 - $3 held by the sibling leaves the child up to $7
	if got, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: f(7)}, human); err != nil || got.BudgetUSD != 7 {
		t.Fatalf("a raise within the parent's room: %v %v", got.BudgetUSD, err)
	}
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: f(1)}, human); err != nil {
		t.Fatalf("lowering is allowed: %v", err)
	}
	_ = other
}
