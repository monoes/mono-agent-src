package tasks

import (
	"context"
	"errors"
	"testing"
)

func policyFor(charter map[int64]bool, levels int) DepthPolicy {
	return func(_ context.Context, id int64) int {
		if charter[id] {
			return levels
		}
		return 0
	}
}

func TestDepthWalkersAtTheCharterDepth(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	ids := []int64{root}
	for i := 1; i < MaxCharterSessionDepth; i++ { // 12 levels
		ids = append(ids, rkid(t, s, db, ids[len(ids)-1], 0, "running"))
	}
	leaf := ids[len(ids)-1]

	// No policy: every walker refuses the tree at the old limit.
	if _, err := s.Commitment(bg, "default", root); !errors.Is(err, ErrLimit) {
		t.Fatalf("commitment without a policy: %v", err)
	}
	if _, err := s.Reserve(bg, "default", leaf, "a", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("reserve without a policy: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: leaf}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("create below the leaf without a policy: %v", err)
	}
	if why, err := s.Overcommitted(bg, db, "default", leaf); err == nil {
		t.Fatalf("overcommitted without a policy: %q", why)
	}

	// A policy for this tree only.
	in := map[int64]bool{}
	for _, id := range ids { // every session of a charter tree carries the charter
		in[id] = true
	}
	s.SetDepthPolicy(policyFor(in, MaxCharterSessionDepth))
	setSpent(t, db, root, 4)
	if _, err := s.Commitment(bg, "default", root); err != nil {
		t.Fatalf("commitment at 12 levels: %v", err)
	}
	r, err := s.Reserve(bg, "default", leaf, "a", "w", 0)
	if err != nil || !near(r.AmountUSD, 6) {
		t.Fatalf("reserve at 12 levels must be bounded by the root's budget: %+v %v", r, err)
	}
	if why, err := s.Overcommitted(bg, db, "default", leaf); err != nil || why != "" {
		t.Fatalf("overcommitted at 12 levels: %q %v", why, err)
	}
	// The 13th level fails closed.
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: leaf}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("level 13: %v", err)
	}
	rkid(t, s, db, leaf, 0, "draft")
	if _, err := s.Commitment(bg, "default", root); !errors.Is(err, ErrLimit) {
		t.Fatalf("a tree of 13 levels: %v", err)
	}
}

func TestDepthPolicyIsClampedAndScopedToTheCharterTree(t *testing.T) {
	s, db, _ := newTestStore(t)
	charter := rsession(t, s, db, 0)
	other := rsession(t, s, db, 0)
	// a policy that asks for 99 gets 12; one that asks for 1 gets today's 4
	s.SetDepthPolicy(func(_ context.Context, id int64) int {
		switch id {
		case charter:
			return 99
		case other:
			return 1
		}
		return 0
	})
	if got := s.depthLimit(bg, charter); got != MaxCharterSessionDepth {
		t.Fatalf("clamped above: %d", got)
	}
	if got := s.depthLimit(bg, other); got != HardMaxSessionDepth {
		t.Fatalf("clamped below: %d", got)
	}
	// a session the policy does not name is refused at the old limit
	cur := other
	for i := 1; i < HardMaxSessionDepth; i++ {
		cur = rkid(t, s, db, cur, 0, "running")
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: cur}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("non-charter session below level 4: %v", err)
	}
}

func TestDepthWalkersStillRefuseACycleAtCharterDepth(t *testing.T) {
	s, db, _ := newTestStore(t)
	a := rsession(t, s, db, 0)
	cur := a
	for i := 1; i < 9; i++ {
		cur = rkid(t, s, db, cur, 0, "draft")
	}
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, cur, a); err != nil {
		t.Fatal(err)
	}
	s.SetDepthPolicy(func(context.Context, int64) int { return MaxCharterSessionDepth })
	if _, err := s.Commitment(bg, "default", a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("commitment on a cycle: %v", err)
	}
	if _, err := s.Reserve(bg, "default", cur, "a", "w", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserve on a cycle: %v", err)
	}
	if _, err := s.sessionChain(bg, db, cur); !errors.Is(err, ErrInvalid) {
		t.Fatalf("chain on a cycle: %v", err)
	}
}
