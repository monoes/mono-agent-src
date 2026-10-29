package tasks

import (
	"errors"
	"testing"
)

// spendAndStop has the child spend its whole budget, then the operator stops it.
func spendAndStop(t *testing.T, s *Store, k Session) {
	t.Helper()
	if _, err := s.AddSessionSpend(bg, "default", k.ID, k.BudgetUSD, "work", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopSession(bg, "default", k.ID, human); err != nil {
		t.Fatal(err)
	}
}

// Review 2, F2: a child's spend never reaches its parent's spent_usd, so a closed child's whole
// budget went back to the pool though it was spent. Six create/spend/close rounds handed a $10
// parent out $30.
func TestAClosedChildsSpendStaysCommittedAgainstTheParent(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := carveParent(t, s, 10)
	var handed float64
	for round := 0; round < 6; round++ {
		k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead)
		if err != nil {
			break
		}
		handed += k.BudgetUSD
		spendAndStop(t, s, k)
	}
	if handed > 10+1e-9 {
		t.Fatalf("a $10 parent handed out $%.2f over six rounds", handed)
	}
}

func TestASpentClosedChildLeavesNothingToGiveAgain(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := carveParent(t, s, 10)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 10}, lead)
	if err != nil {
		t.Fatal(err)
	}
	spendAndStop(t, s, k)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "again", ParentSessionID: p.ID, BudgetUSD: 1}, lead); !errors.Is(err, ErrLimit) {
		t.Fatalf("the spent $10 came back: %v", err)
	}
}

// Review 2, smaller (a): an update clamps only what it lowered, and never below a child's spend.
func TestAnUnrelatedParentEditLeavesAChildsBudgetAlone(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, _ := carveParent(t, s, 10)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 5}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", k.ID, 4, "work", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", p.ID, 6, "work", human); err != nil {
		t.Fatal(err)
	}
	mt := 100
	if _, err := s.UpdateSession(bg, "default", p.ID, SessionUpdate{MaxTasks: &mt}, human); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(bg, "default", k.ID, human); got.BudgetUSD != 5 {
		t.Fatalf("an edit of max_tasks changed the child's budget to $%.2f", got.BudgetUSD)
	}
}

func TestLoweringTheBudgetNeverClampsAChildBelowItsSpend(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, _ := carveParent(t, s, 10)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 5}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", k.ID, 4, "work", human); err != nil {
		t.Fatal(err)
	}
	b := 2.0
	if _, err := s.UpdateSession(bg, "default", p.ID, SessionUpdate{BudgetUSD: &b}, human); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(bg, "default", k.ID, human); got.BudgetUSD != 4 {
		t.Fatalf("the child's budget is $%.2f, want its $4 spend", got.BudgetUSD)
	}
}
