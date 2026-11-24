package coderhost

import (
	"errors"
	"testing"
)

func lowerMaxDepth(t *testing.T, st *Store, c Caller, d int) {
	t.Helper()
	s, _ := st.GetSettings("p", c.ConversationID)
	s.MaxDepth = d
	if err := st.SaveSettings("p", c.ConversationID, s); err != nil {
		t.Fatal(err)
	}
}

// A master's lower max_depth set after its children exist bounds the whole subtree (#1761): the child, which stored the
// master's old depth, may not spawn, and no grandchild is created.
func TestMasterLowerMaxDepthCapsItsExistingSubtree(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 3, 2) // master + child, both max_depth 3
	lowerMaxDepth(t, st, tree[0], 1)
	_, err := st.Spawn(tree[1], SpawnRequest{})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("a child spawned under a master whose max_depth is 1: %v", err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE parent_conversation_id = ?`, tree[1].ConversationID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d grandchildren exist", n)
	}
	// the default (3) still allows a level-3 session when no ancestor is lower
	st2 := newStoreT(t)
	t2 := chainTo(t, st2, "coding", 3, 2)
	if _, err := st2.Spawn(t2[1], SpawnRequest{}); err != nil {
		t.Fatalf("default depth 3 refused a level-3 session: %v", err)
	}
}

// The operator may raise a single child's max_depth only up to what its ancestors allow.
func TestOperatorCannotRaiseAChildPastItsAncestors(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 2, 2)
	s, _ := st.GetSettings("p", tree[1].ConversationID)
	s.MaxDepth = 4
	if err := st.SaveSettings("p", tree[1].ConversationID, s); err == nil {
		t.Fatal("a child's max_depth was raised past its master's")
	}
	s.MaxDepth = 2
	if err := st.SaveSettings("p", tree[1].ConversationID, s); err != nil {
		t.Fatalf("a child's max_depth within its master's was refused: %v", err)
	}
}

// The cap is the minimum along the whole chain, not just the parent.
func TestMaxDepthCapIsTheMinimumAlongTheChain(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 3)
	lowerMaxDepth(t, st, tree[1], 3)
	lowerMaxDepth(t, st, tree[0], 2)
	if _, err := st.Spawn(tree[2], SpawnRequest{}); err == nil {
		t.Fatal("level 3 spawned although the root's max_depth is 2")
	}
	if got := st.effectiveMaxDepth("p", tree[2].ConversationID, 4); got != 2 {
		t.Fatalf("effective max_depth = %d, want 2", got)
	}
}
