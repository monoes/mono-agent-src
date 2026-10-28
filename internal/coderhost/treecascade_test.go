package coderhost

import "testing"

func stateOf(t *testing.T, st *Store, conv string) State {
	t.Helper()
	x, ok, err := st.GetSession("p", conv)
	if err != nil || !ok {
		t.Fatalf("session %s: %v %v", conv, ok, err)
	}
	return x.State
}

func forceState(t *testing.T, st *Store, conv string, s State) {
	t.Helper()
	x, _, _ := st.GetSession("p", conv)
	x.State = s
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
}

func TestStoppingAParentStopsEveryDescendantAtDepthThreeAndFour(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 4)
	h := &Host{Store: st, L: newFakeLauncher()}
	forceState(t, st, tree[0].ConversationID, StateStopped)
	h.treeCascadePass()
	for lvl := 1; lvl < 4; lvl++ {
		if got := stateOf(t, st, tree[lvl].ConversationID); got != StateStopped {
			t.Fatalf("level %d is %s after its root was stopped", lvl+1, got)
		}
	}
}

func TestStoppingAMiddleSessionLeavesTheRootAndItsSiblingsRunning(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 4)
	sibling := spawn(t, st, tree[0])
	h := &Host{Store: st, L: newFakeLauncher()}
	forceState(t, st, tree[1].ConversationID, StateStopped)
	h.treeCascadePass()
	if stateOf(t, st, tree[2].ConversationID) != StateStopped || stateOf(t, st, tree[3].ConversationID) != StateStopped {
		t.Fatal("levels 3 and 4 outlived their level 2")
	}
	if stateOf(t, st, tree[0].ConversationID) == StateStopped || stateOf(t, st, sibling) == StateStopped {
		t.Fatal("the cascade went up or sideways")
	}
}

func TestPausingAParentPausesEveryDescendantAtDepthThreeAndFour(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 4)
	l := newFakeLauncher()
	h := &Host{Store: st, L: l}
	forceState(t, st, tree[0].ConversationID, StatePaused)
	h.treeCascadePass()
	for lvl := 1; lvl < 4; lvl++ {
		if !st.hasPendingCascade("p", tree[lvl].ConversationID, KindPause) {
			t.Fatalf("level %d has no pause queued", lvl+1)
		}
	}
	h.treeCascadePass() // a second pass queues nothing more
	if n := st.PendingFor("p", tree[3].ConversationID); n != 1 {
		t.Fatalf("level 4 has %d pending commands", n)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	for lvl := 1; lvl < 4; lvl++ {
		if got := stateOf(t, st, tree[lvl].ConversationID); got != StatePaused {
			t.Fatalf("level %d is %s after its root was paused", lvl+1, got)
		}
	}
	if stateOf(t, st, tree[0].ConversationID) != StatePaused {
		t.Fatal("the root itself changed")
	}
}

func TestAPersonsStopOnTheRootStopsTheWholeTree(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 4)
	l := newFakeLauncher()
	h := &Host{Store: st, L: l}
	// the root runs a turn, a person stops it
	cmd, err := st.Enqueue(Command{ProfileID: "p", ConversationID: tree[0].ConversationID, Kind: KindSend, TurnID: "t-1", Message: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	_ = cmd
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: tree[0].ConversationID, Kind: KindStop}); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	h.Wait()
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	for lvl := 1; lvl < 4; lvl++ {
		if got := stateOf(t, st, tree[lvl].ConversationID); got != StateStopped {
			t.Fatalf("level %d is %s after a person stopped the root", lvl+1, got)
		}
	}
}
