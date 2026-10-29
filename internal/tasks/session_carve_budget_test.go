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
