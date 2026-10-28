package tasks

import (
	"context"
	"sync/atomic"
	"testing"
)

// A caller the transaction will refuse anyway (not the holder, session paused) never starts the
// git check; the authorized holder does.
func TestUnauthorizedCallerNeverRunsTheChecker(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := evTask(t, s, codeNotes)
	var calls atomic.Int32
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error {
		calls.Add(1)
		return nil
	})
	// not the holder: a finish and a commit attach
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w2")); err == nil {
		t.Fatal("a non-holder finished the task")
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "abc"}, bot("w2")); err == nil {
		t.Fatal("a non-holder attached a commit")
	}
	// a session that does not run refuses its agents
	if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); err == nil {
		t.Fatal("an agent finished a task of a paused session")
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "abc"}, bot("w1")); err == nil {
		t.Fatal("an agent attached a commit in a paused session")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the checker ran %d times for callers who were refused anyway", n)
	}
	if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); err != nil {
		t.Fatalf("the holder: %v", err)
	}
	if calls.Load() == 0 {
		t.Fatal("the holder's finish was never checked")
	}
}
