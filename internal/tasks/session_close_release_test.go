package tasks

import "testing"

// Closing a session gives its claims back as a stop does: nothing stays held by a worker of
// a session that is over.
func TestSessionCloseReleasesAllClaims(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, _ := runningWith(t, s, "a", "b")
	claimNext(t, s, se.ID, "w1")
	claimNext(t, s, se.ID, "w2")
	if n := countWhere(t, db, "tasks", "session_id = ? AND claimed_by <> ''", se.ID); n != 2 {
		t.Fatalf("setup: %d claims", n)
	}
	got, err := s.CloseSession(bg, "default", se.ID, SessionDone, "ok", human)
	if err != nil || got.State != SessionDone {
		t.Fatalf("close: %+v %v", got, err)
	}
	if n := countWhere(t, db, "tasks", "session_id = ? AND claimed_by <> ''", se.ID); n != 0 {
		t.Fatalf("%d claims survived the close", n)
	}
	if n := countWhere(t, db, "tasks", "session_id = ? AND status = 'ready'", se.ID); n != 2 {
		t.Fatalf("released tasks go to Ready: %d ready", n)
	}
}

// The final-report reference is the operator's process's: an agent cannot attach it, the
// operator can, and any other report is open to the holder as before.
func TestAddArtifactReservesTheFinalReportRef(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, _ := runningWith(t, s, "a")
	tk := claimNext(t, s, se.ID, "w1")
	w := bot("w1")
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "report", Ref: FinalReportRef, Note: "fake"}, w); err == nil {
		t.Fatal("an agent attached the reserved final report")
	}
	if n := countWhere(t, db, "task_artifacts", "task_id = ?", tk.ID); n != 0 {
		t.Fatalf("%d artifacts written by the refused call", n)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "report", Ref: "notes.md"}, w); err != nil {
		t.Fatalf("an ordinary report: %v", err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "report", Ref: FinalReportRef, Note: "real"}, human); err != nil {
		t.Fatalf("the operator: %v", err)
	}
}

// close_proposed is written by the operator's process only.
func TestCloseProposedEventIsOperatorOnly(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, _ := runningWith(t, s, "a")
	if err := s.AddSessionEvent(bg, "default", se.ID, EventCloseProposed, "x", bot("lead")); err == nil {
		t.Fatal("an agent wrote close_proposed")
	}
	if err := s.AddSessionEvent(bg, "default", se.ID, EventCloseProposed, "x", human); err != nil {
		t.Fatal(err)
	}
}
