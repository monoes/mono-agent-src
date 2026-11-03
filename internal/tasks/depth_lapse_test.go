package tasks

import (
	"context"
	"errors"
	"testing"
)

// A charter's tree outlives its run: once the policy lapses (the run is quiescent) the sessions at levels 5 to 8 must
// still be readable, settled, closed and archived. Only creating a session below the hard limit stays refused.
func TestADeepCharterTreeStaysReadableAndClosableAfterThePolicyLapses(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.SetDepthPolicy(func(context.Context, int64) int { return MaxCharterSessionDepth })
	cur := mustSession(t, s, "default", "l1")
	ids := []int64{cur.ID}
	for level := 2; level <= 8; level++ {
		next, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: cur.ID}, human)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		cur = next
		ids = append(ids, next.ID)
	}

	s.SetDepthPolicy(func(context.Context, int64) int { return 0 }) // the run ended: the policy lapses

	for i, id := range ids {
		if _, err := s.GetSession(bg, "default", id, human); err != nil {
			t.Fatalf("level %d unreadable after the lapse: %v", i+1, err)
		}
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: ids[7]}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("creating below level 8 after the lapse must stay refused: %v", err)
	}
	for i := len(ids) - 1; i >= 0; i-- {
		if _, err := s.StopSession(bg, "default", ids[i], human); err != nil {
			t.Fatalf("stopping level %d after the lapse: %v", i+1, err)
		}
	}
	if _, err := s.ArchiveSessionCascade(bg, "default", ids[0], human); err != nil {
		t.Fatalf("archiving the whole tree after the lapse: %v", err)
	}
	for i := 4; i < 8; i++ {
		if _, err := s.GetSession(bg, "default", ids[i], human); err != nil {
			t.Fatalf("archived level %d unreadable: %v", i+1, err)
		}
	}
}
