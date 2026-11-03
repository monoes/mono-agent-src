package coderhost

import (
	"context"
	"database/sql"
	"testing"
)

func charterDepth(n int) DepthPolicy {
	return func(string, string) int { return n }
}

func setMaxDepth(st *Store, c Caller, n int) error {
	_, err := st.UpdateSettings("p", c.ConversationID, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		cur.MaxDepth = n
		return cur, nil
	})
	return err
}

func TestCharterTreeGoesTwelveLevelsAndNoDeeper(t *testing.T) {
	st := newStoreT(t)
	st.SetDepthPolicy(charterDepth(12))
	root, _ := master(t, st, "coding")
	if err := setMaxDepth(st, root, 12); err != nil {
		t.Fatalf("max_depth 12 under a charter: %v", err)
	}
	tree := []Caller{root}
	for len(tree) < 12 {
		tree = append(tree, asChild(spawn(t, st, tree[len(tree)-1])))
	}
	if s, _ := st.GetSettings("p", tree[11].ConversationID); s.MaxDepth != 12 || s.MaxChildren != 0 {
		t.Fatalf("level 12: %+v", s)
	}
	if _, err := st.Spawn(tree[11], SpawnRequest{}); err == nil {
		t.Fatal("level 12 spawned a level 13")
	}
}

func TestPolicyAbove12IsClampedAndNoPolicyKeepsFour(t *testing.T) {
	st := newStoreT(t)
	st.SetDepthPolicy(charterDepth(99))
	if got := st.depthCeiling("p", "c"); got != 12 {
		t.Fatalf("clamped to %d", got)
	}
	st.SetDepthPolicy(charterDepth(1))
	if got := st.depthCeiling("p", "c"); got != MaxSessionDepth {
		t.Fatalf("a policy cannot lower the old limit: %d", got)
	}
	st.SetDepthPolicy(nil)
	root, _ := master(t, st, "coding")
	if err := setMaxDepth(st, root, 5); err == nil {
		t.Fatal("max_depth 5 accepted without a charter")
	}
	s := DefaultSettings()
	s.MaxDepth = 5
	if err := s.Validate(); err == nil {
		t.Fatal("Validate accepts max_depth above the hard limit")
	}
	if _, _, err := s.applyAt([]byte(`{"max_depth": 12}`), MaxSessionDepth); err == nil {
		t.Fatal("Apply accepted 12 at the old ceiling")
	}
	if _, _, err := s.applyAt([]byte(`{"max_depth": 12}`), 12); err != nil {
		t.Fatalf("Apply at ceiling 12: %v", err)
	}
}

// A charter session keeps the max_depth it was given after the run ends: its settings stay readable and writable, and
// the lapse does not let anything be raised.
func TestADeepCharterSessionsSettingsStayReadableAfterThePolicyLapses(t *testing.T) {
	st := newStoreT(t)
	st.SetDepthPolicy(charterDepth(12))
	root, _ := master(t, st, "coding")
	if err := setMaxDepth(st, root, 8); err != nil {
		t.Fatal(err)
	}
	st.SetDepthPolicy(charterDepth(0)) // the run ended
	if s, err := st.GetSettings("p", root.ConversationID); err != nil || s.MaxDepth != 8 {
		t.Fatalf("settings after the lapse: %+v %v", s, err)
	}
	if _, err := st.UpdateSettings("p", root.ConversationID, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		cur.MaxDepth = 6 // lowering is allowed
		return cur, nil
	}); err != nil {
		t.Fatalf("lowering after the lapse: %v", err)
	}
	if err := setMaxDepth(st, root, 8); err == nil {
		t.Fatal("raising max_depth after the lapse was accepted")
	}
}
