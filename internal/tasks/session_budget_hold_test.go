package tasks

import (
	"errors"
	"testing"
)

// Review of PR #1141, P1: a $10 parent whose live child holds the whole $10 cannot claim a task of its own.
func TestAParentCannotWorkWithABudgetItHandedToAChild(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, err := s.CreateSession(bg, "default", SessionInput{Title: "parent", BudgetUSD: 10}, human)
	if err != nil {
		t.Fatal(err)
	}
	mustTask(t, s, "default", p.ID, "own work")
	mustRun(t, s, "default", p)
	k := kidOf(t, s, p.ID, 10)
	if _, err := s.SessionNext(bg, "default", p.ID, bot("w"), true, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("the parent claimed its own task with its whole budget held by a child: %v", err)
	}
	if n, err := s.SessionReady(bg, "default", p.ID, human); err != nil || len(n) != 0 {
		t.Fatalf("the parent lists work: %v %v", n, err)
	}
	// the child's budget is released when it stops unspent: the parent works again
	stopSession(t, s, k.ID)
	if tk, err := s.SessionNext(bg, "default", p.ID, bot("w"), true, 0); err != nil || tk == nil {
		t.Fatalf("the parent could not work after the child stopped unspent: %v %v", tk, err)
	}
}

// A child that holds only part leaves the parent the rest.
func TestAParentWorksWithWhatItKept(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, err := s.CreateSession(bg, "default", SessionInput{Title: "parent", BudgetUSD: 10}, human)
	if err != nil {
		t.Fatal(err)
	}
	mustTask(t, s, "default", p.ID, "own work")
	mustRun(t, s, "default", p)
	kidOf(t, s, p.ID, 4)
	if tk, err := s.SessionNext(bg, "default", p.ID, bot("w"), true, 0); err != nil || tk == nil {
		t.Fatalf("a parent with $6 of its $10 kept was refused: %v %v", tk, err)
	}
}
