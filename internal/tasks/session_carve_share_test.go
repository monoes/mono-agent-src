package tasks

import (
	"errors"
	"testing"
)

// Review 2, F3: left in [$0.01, $0.02) gave a default share of 0, and a child budget of 0 is no cap.
func TestADefaultShareBelowACentIsRefusedNotUncapped(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := carveParent(t, s, 0.015)
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead)
	if err == nil {
		t.Fatalf("a $0.015 parent gave a child budget $%.4f (0 means no cap)", k.BudgetUSD)
	}
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("want a limit error: %v", err)
	}
}
