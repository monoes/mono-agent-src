package coderhost

import "testing"

func pendingResumes(t *testing.T, st *Store, conv string) int {
	t.Helper()
	return countRows(t, st, `SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ? AND kind = 'resume' AND status = 'pending'`, conv)
}

// The mission pass paused the task child for the operator's pause of the mission; the tree pass, which sees the
// lead running, must not take that pause for its own and lift it.
func TestTreePassDoesNotResumeATaskChildTheMissionPassPaused(t *testing.T) {
	f := newHostFix(t)
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, f.st, a.ConversationID) != StatePaused {
		t.Fatal("the mission pass did not pause the child")
	}
	f.h.treeCascadePass()
	if n := pendingResumes(t, f.st, a.ConversationID); n != 0 {
		t.Fatalf("the tree pass queued %d resumes for a child the mission pass owns", n)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, f.st, a.ConversationID) != StatePaused {
		t.Fatal("the child was resumed while its mission is paused")
	}
	f.finish()
}

func TestTreePassResumesWhatItsOwnPauseStopped(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 2)
	h := &Host{Store: st, L: newFakeLauncher()}
	child := tree[1].ConversationID
	forceState(t, st, tree[0].ConversationID, StatePaused)
	h.treeCascadePass()
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, st, child) != StatePaused {
		t.Fatal("the tree pass did not pause the grandchild")
	}
	forceState(t, st, tree[0].ConversationID, StateIdle)
	h.treeCascadePass()
	if n := pendingResumes(t, st, child); n != 1 {
		t.Fatalf("pending resumes = %d, want 1", n)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, st, child) == StatePaused {
		t.Fatal("the tree pass did not lift its own pause")
	}
}

func TestTreePassNeverLiftsAPersonsPause(t *testing.T) {
	st := newStoreT(t)
	tree := chainTo(t, st, "coding", 4, 2)
	h := &Host{Store: st, L: newFakeLauncher()}
	child := tree[1].ConversationID
	forceState(t, st, tree[0].ConversationID, StatePaused)
	h.treeCascadePass()
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: child, Kind: KindPause}); err != nil { // a person's, after the pass's
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	forceState(t, st, tree[0].ConversationID, StateIdle)
	h.treeCascadePass()
	if n := pendingResumes(t, st, child); n != 0 {
		t.Fatalf("the tree pass queued %d resumes over a person's pause", n)
	}
}

// A pause carrying the mission pass's turn id is not the tree pass's to lift, even for a session with no mission row.
func TestTreePassDoesNotLiftAPauseWithTheMissionPassTurnId(t *testing.T) {
	st := newStoreT(t)
	lead, _ := master(t, st, "workspace")
	child := spawn(t, st, lead)
	h := &Host{Store: st, L: newFakeLauncher()}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: child, Kind: KindPause, TurnID: cascadeTurn}); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	h.treeCascadePass()
	if n := pendingResumes(t, st, child); n != 0 {
		t.Fatalf("the tree pass queued %d resumes for the mission pass's pause", n)
	}
}

// A task child of a mission lead is also under the tree pass: a paused lead conversation pauses it, and the same pass
// resumes it, as it is that pass's pause.
func TestTreePassPausesAndResumesATaskChildForAPausedLead(t *testing.T) {
	f := newHostFix(t)
	a := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	forceState(t, f.st, f.lead.ConversationID, StatePaused)
	f.h.treeCascadePass()
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, f.st, a.ConversationID) != StatePaused {
		t.Fatal("the paused lead did not pause its task child")
	}
	forceState(t, f.st, f.lead.ConversationID, StateIdle)
	f.h.treeCascadePass()
	if n := pendingResumes(t, f.st, a.ConversationID); n != 1 {
		t.Fatalf("pending resumes = %d, want 1", n)
	}
	f.finish()
}
