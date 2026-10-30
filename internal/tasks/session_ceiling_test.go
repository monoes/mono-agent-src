package tasks

import (
	"errors"
	"strings"
	"testing"
)

// The codex probe: root $10, child $8, grandchild $8; the child is stopped, the root is lowered to $3.
// The grandchild may not be raised back to $8 through the stopped child's stale $8.
func TestAGrandchildIsNotRaisedAboveALoweredRootThroughAStoppedChild(t *testing.T) {
	s, _, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "coding"}, []float64{10, 8, 8})
	if _, err := s.StopSession(bg, "default", c[1].ID, human); err != nil {
		t.Fatal(err)
	}
	b := 3.0
	lowerRoot(t, s, c[0].ID, SessionUpdate{BudgetUSD: &b})
	up := 8.0
	if _, err := s.UpdateSession(bg, "default", c[2].ID, SessionUpdate{BudgetUSD: &up}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("raising the grandchild to $8 under a root of $3: err = %v, want a limit", err)
	}
	wantLimits(t, s, c[2].ID, "coding", 3)
}

// The same for access, the task cap and the depth cap.
func TestAGrandchildIsNotRaisedAboveALoweredRootInAccessOrCapsThroughAStoppedChild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lower SessionUpdate
		raise SessionUpdate
	}{
		{"access", SessionUpdate{AccessLevel: ptr("scoped")}, SessionUpdate{AccessLevel: ptr("coding")}},
		{"max tasks", SessionUpdate{MaxTasks: ptr(5)}, SessionUpdate{MaxTasks: ptr(50)}},
		{"max depth", SessionUpdate{MaxDepth: ptr(1)}, SessionUpdate{MaxDepth: ptr(2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			c := chainOf(t, s, []string{"full", "full", "full"}, []float64{0, 0, 0})
			for _, id := range []int64{c[1].ID, c[2].ID} {
				if _, err := s.UpdateSession(bg, "default", id, SessionUpdate{MaxTasks: ptr(100), MaxDepth: ptr(3)}, human); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.StopSession(bg, "default", c[1].ID, human); err != nil {
				t.Fatal(err)
			}
			lowerRoot(t, s, c[0].ID, tc.lower)
			if _, err := s.UpdateSession(bg, "default", c[2].ID, tc.raise, human); !errors.Is(err, ErrLimit) {
				t.Fatalf("err = %v, want a limit", err)
			}
		})
	}
}

// A parent cycle (a store edited by hand) fails the ceiling walk closed, with the cycle error; it does
// not loop and does not fall back to a partial answer.
func TestTheCeilingWalkFailsClosedOnAParentCycle(t *testing.T) {
	s, db, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "coding"}, []float64{10, 8, 8})
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, c[2].ID, c[0].ID); err != nil {
		t.Fatal(err)
	}
	err := s.snapshot(bg, func(x dbx) error {
		_, err := s.ceilingAt(bg, x, "default", c[2].ID)
		return err
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v, want the cycle error", err)
	}
	b := 5.0
	if _, err := s.UpdateSession(bg, "default", c[2].ID, SessionUpdate{BudgetUSD: &b}, human); err == nil {
		t.Fatal("an update below a cycle was accepted")
	}
}
