package tasks

import (
	"errors"
	"testing"
)

func kidOf(t *testing.T, s *Store, parent int64, budget float64) Session {
	t.Helper()
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: parent, BudgetUSD: budget}, human)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func stopSession(t *testing.T, s *Store, id int64) {
	t.Helper()
	if _, err := s.StopSession(bg, "default", id, human); err != nil {
		t.Fatal(err)
	}
}

func wantNoRoom(t *testing.T, s *Store, parent int64, what string) {
	t.Helper()
	_, err := s.CreateSession(bg, "default", SessionInput{Title: "again", ParentSessionID: parent, BudgetUSD: 1}, human)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("%s: a new $1 child was not refused: %v", what, err)
	}
}

// Review of PR #1141, P1: a $10 root, a $10 child and a $10 grandchild; the grandchild spends $10 and
// both stop. The $10 is spent, so the root has nothing to give again.
func TestAGrandchildsSpendStaysHeldAfterBothStop(t *testing.T) {
	s, _, _ := newTestStore(t)
	root, _ := carveParent(t, s, 10)
	c := kidOf(t, s, root.ID, 10)
	g := kidOf(t, s, c.ID, 10)
	spendAndStop(t, s, g)
	stopSession(t, s, c.ID)
	wantNoRoom(t, s, root.ID, "after the grandchild spent $10 and both stopped")
}

// Stopping the intermediate child while the grandchild is still open does not release the
// grandchild's live commitment.
func TestStoppingAnIntermediateKeepsAnOpenGrandchildsBudgetHeld(t *testing.T) {
	s, _, _ := newTestStore(t)
	root, _ := carveParent(t, s, 10)
	c := kidOf(t, s, root.ID, 10)
	kidOf(t, s, c.ID, 10)
	stopSession(t, s, c.ID)
	wantNoRoom(t, s, root.ID, "with the intermediate stopped and its grandchild open")
}

// The same at the deepest level the tree allows (four levels), spend and open commitment both.
func TestTheHoldRecursesToTheDeepestLevel(t *testing.T) {
	s, _, _ := newTestStore(t)
	root, _ := carveParent(t, s, 10)
	c := kidOf(t, s, root.ID, 10)
	g := kidOf(t, s, c.ID, 10)
	gg := kidOf(t, s, g.ID, 10)
	stopSession(t, s, c.ID)
	stopSession(t, s, g.ID)
	wantNoRoom(t, s, root.ID, "level 4 open below two stopped sessions")
	spendAndStop(t, s, gg)
	wantNoRoom(t, s, root.ID, "level 4 spent and stopped")
}

// A closed intermediate that spent part of its budget hands the rest back only as far as nothing below
// it holds: here $2 spent by the child, $3 spent by its grandchild, so $5 stays held.
func TestAClosedSubtreeHoldsExactlyItsSpend(t *testing.T) {
	s, _, _ := newTestStore(t)
	root, _ := carveParent(t, s, 10)
	c := kidOf(t, s, root.ID, 10)
	g := kidOf(t, s, c.ID, 5)
	if _, err := s.AddSessionSpend(bg, "default", c.ID, 2, "work", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionSpend(bg, "default", g.ID, 3, "work", human); err != nil {
		t.Fatal(err)
	}
	stopSession(t, s, g.ID)
	stopSession(t, s, c.ID)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "ok", ParentSessionID: root.ID, BudgetUSD: 5}, human); err != nil {
		t.Fatalf("the $5 that was not spent is free: %v", err)
	}
	wantNoRoom(t, s, root.ID, "after the free $5 went out")
}
