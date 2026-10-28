package coderhost

import (
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// chainTo builds a tree of one session per level down to level n from a master whose working depth is maxDepth. It
// returns the callers, the root first.
func chainTo(t *testing.T, st *Store, access string, maxDepth, n int) []Caller {
	t.Helper()
	root, _ := master(t, st, access)
	s, _ := st.GetSettings("p", root.ConversationID)
	s.MaxDepth = maxDepth
	if err := st.SaveSettings("p", root.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	out := []Caller{root}
	for len(out) < n {
		id := spawn(t, st, out[len(out)-1])
		out = append(out, asChild(id))
	}
	return out
}

func TestHardMaxDepthIsTheTaskTreesOneSourceOfTruth(t *testing.T) {
	hard := tasks.HardMaxSessionDepth
	if got := MaxSessionDepth; got != hard || got != 4 {
		t.Fatalf("MaxSessionDepth = %d, the task tree's is %d", got, hard)
	}
	if got := DefaultSettings().MaxDepth; got != 3 {
		t.Fatalf("the working depth defaults to %d, want 3", got)
	}
	s := DefaultSettings()
	s.MaxDepth = 4
	if err := s.Validate(); err != nil {
		t.Fatalf("the operator may raise max_depth to the hard limit: %v", err)
	}
	s.MaxDepth = 5
	if err := s.Validate(); err == nil {
		t.Fatal("max_depth above the hard limit was accepted")
	}
}

func TestSessionAtTheWorkingDepthCannotSpawn(t *testing.T) {
	for _, tc := range []struct{ depth, level int }{{3, 3}, {4, 4}, {2, 2}} {
		st := newStoreT(t)
		tree := chainTo(t, st, "coding", tc.depth, tc.level)
		_, err := st.Spawn(tree[tc.level-1], SpawnRequest{})
		if !errors.Is(err, ErrLimit) && !errors.Is(err, ErrNotYours) {
			t.Fatalf("a session at level %d of a depth-%d tree spawned: %v", tc.level, tc.depth, err)
		}
		if s, _ := st.GetSettings("p", tree[tc.level-1].ConversationID); tc.level > 1 && s.MaxChildren != 0 {
			t.Fatalf("a session at the working depth has max_children %d, want 0", s.MaxChildren)
		}
	}
}

func TestSessionsBelowTheWorkingDepthMaySpawn(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 4) // levels 1 to 4 all exist
	for i, c := range tree[:3] {
		s, _ := st.GetSettings("p", c.ConversationID)
		if s.MaxChildren == 0 {
			t.Fatalf("level %d has no room for children below the working depth 4", i+1)
		}
		if s.MaxDepth != 4 {
			t.Fatalf("level %d lost the operator's working depth: %d", i+1, s.MaxDepth)
		}
	}
	if s, _ := st.GetSettings("p", tree[3].ConversationID); s.MaxChildren != 0 {
		t.Fatalf("level 4 has max_children %d", s.MaxChildren)
	}
	if _, err := st.Spawn(tree[3], SpawnRequest{}); err == nil {
		t.Fatal("level 4 spawned a level 5")
	}
}

func TestAChildCannotWidenTheOperatorsDepth(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 3, 2)
	// the child (level 2) may not raise its own working depth: max_depth is the operator's
	if _, _, err := st.SetChild(tree[0], tree[1].ConversationID, []byte(`{"max_depth":4}`)); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a master raised a child's max_depth: %v", err)
	}
	s, _ := st.GetSettings("p", tree[1].ConversationID)
	if s.MaxDepth != 3 {
		t.Fatalf("child max_depth = %d, want the root's 3", s.MaxDepth)
	}
}

func TestAChildNeverHasMoreRoomThanItsParent(t *testing.T) {
	st := newStoreT(t)
	root, _ := master(t, st, "coding")
	s, _ := st.GetSettings("p", root.ConversationID)
	s.MaxChildren, s.MaxConcurrent = 1, 1
	if err := st.SaveSettings("p", root.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	kid := spawn(t, st, root)
	ks, _ := st.GetSettings("p", kid)
	if ks.MaxChildren != 1 || ks.MaxConcurrent != 1 {
		t.Fatalf("child room = %d children, %d concurrent; the parent's is 1 and 1", ks.MaxChildren, ks.MaxConcurrent)
	}
}

func TestAccessNeverRisesDownTheTree(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "full", 4, 2) // a full-access root hands out workspace at most
	l2 := tree[1]
	out, err := st.Spawn(l2, SpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Access != "workspace" {
		t.Fatalf("level 3 access = %s", out.Access)
	}
	l3 := asChild(out.ConversationID)
	if _, err := st.Spawn(l3, SpawnRequest{Access: "full"}); !errors.Is(err, ErrAccess) && !errors.Is(err, ErrLimit) {
		t.Fatalf("a full-access child at level 4: %v", err)
	}
	st2 := newStoreT(t)
	tree2 := chainTo(t, st2, "workspace", 4, 2)
	o3, err := st2.Spawn(tree2[1], SpawnRequest{Access: "coding"})
	if err != nil || o3.Access != "coding" {
		t.Fatalf("level 3 = %+v, %v", o3, err)
	}
	if _, err := st2.Spawn(asChild(o3.ConversationID), SpawnRequest{Access: "workspace"}); !errors.Is(err, ErrAccess) {
		t.Fatalf("level 4 asked for more than its parent has: %v", err)
	}
}

func TestBudgetIsCarvedDownTheTree(t *testing.T) {
	st := newStoreT(t)
	root, _ := master(t, st, "coding")
	rs, _ := st.GetSettings("p", root.ConversationID)
	rs.MaxDepth = 4
	st.SaveSettings("p", root.ConversationID, rs)
	st.SetBudget("p", root.ConversationID, 10)
	l2, err := st.Spawn(root, SpawnRequest{BudgetUSD: 6})
	if err != nil {
		t.Fatal(err)
	}
	c2 := asChild(l2.ConversationID)
	l3, err := st.Spawn(c2, SpawnRequest{BudgetUSD: 4})
	if err != nil {
		t.Fatal(err)
	}
	c3 := asChild(l3.ConversationID)
	if _, err := st.Spawn(c3, SpawnRequest{BudgetUSD: 5}); !errors.Is(err, ErrLimit) {
		t.Fatalf("level 4 asked for $5 of level 3's $4: %v", err)
	}
	if _, err := st.Spawn(c2, SpawnRequest{BudgetUSD: 3}); !errors.Is(err, ErrLimit) {
		t.Fatalf("level 2 had $6, gave $4, and then gave $3 more: %v", err)
	}
	if _, err := st.Spawn(c3, SpawnRequest{BudgetUSD: 4}); err != nil {
		t.Fatalf("level 4 with all of level 3's $4: %v", err)
	}
}

func TestMaxChildrenAndRateHoldAtDepth(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	tree := chainTo(t, st, "coding", 4, 3)
	l3 := tree[2]
	s, _ := st.GetSettings("p", l3.ConversationID)
	s.MaxChildren = 2
	if err := st.SaveSettings("p", l3.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	spawn(t, st, l3)
	spawn(t, st, l3)
	if _, err := st.Spawn(l3, SpawnRequest{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a level-3 session with max_children 2 made a third: %v", err)
	}
	s.MaxChildren = 20
	if err := st.SaveSettings("p", l3.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	var err error
	for i := 0; i < SpawnRateMax+1 && err == nil; i++ {
		_, err = st.Spawn(l3, SpawnRequest{})
	}
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("the spawn rate did not hold at level 3: %v", err)
	}
	now = now.Add(SpawnRateWindow + time.Minute)
	if _, err := st.Spawn(l3, SpawnRequest{}); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestLiveSessionsPerTreeAreCapped(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	root, _ := master(t, st, "coding")
	s, _ := st.GetSettings("p", root.ConversationID)
	s.MaxChildren = 20
	if err := st.SaveSettings("p", root.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	var kids []string
	for i := 0; i < MaxLiveSessionsPerTree; i++ {
		now = now.Add(SpawnRateWindow + time.Minute) // keep the rate limit out of this test
		kids = append(kids, spawn(t, st, root))
	}
	now = now.Add(SpawnRateWindow + time.Minute)
	if _, err := st.Spawn(root, SpawnRequest{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a 13th live session: %v", err)
	}
	// the cap counts the whole tree: a session deep in it is refused too
	now = now.Add(SpawnRateWindow + time.Minute)
	if _, err := st.Spawn(asChild(kids[0]), SpawnRequest{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a level-2 spawn into a full tree: %v", err)
	}
	// a refused spawn left nothing behind
	if n, _ := st.liveInTree("p", root.ConversationID); n != MaxLiveSessionsPerTree+1 {
		t.Fatalf("live sessions = %d", n)
	}
	// a stopped session no longer counts
	x, _, _ := st.GetSession("p", kids[1])
	x.State = StateStopped
	st.SaveSession(x)
	now = now.Add(SpawnRateWindow + time.Minute)
	if _, err := st.Spawn(root, SpawnRequest{}); err != nil {
		t.Fatalf("after one stopped: %v", err)
	}
}
