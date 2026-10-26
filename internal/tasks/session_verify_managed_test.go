package tasks

import (
	"errors"
	"testing"
)

// managedReviewTask is reviewTask in a session a lead created for a conversation.
func managedReviewTask(t *testing.T, s *Store) (Session, Task) {
	t.Helper()
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "managed", Goal: "g", ConversationID: "conv-1"}, human)
	if err != nil {
		t.Fatal(err)
	}
	tk := mustTask(t, s, "default", se.ID, "work")
	mustRun(t, s, "default", se)
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, bot("m1-t1"), 0); err != nil {
		t.Fatal(err)
	}
	finishTask(t, s, se.ID, tk.ID, "m1-t1")
	return se, tk
}

// An agent with no binding cannot record a verdict in a session that a conversation manages:
// only that task's verifier binding (or the operator) can, so dropping a binding gains nothing.
func TestUnboundAgentCannotVerifyInAManagedSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := managedReviewTask(t, s)
	for _, pass := range []bool{true, false} {
		_, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, pass, "looks fine", bot("someone-else"))
		if !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("pass=%v: want a refusal, got %v", pass, err)
		}
	}
	if got, _, _ := s.Get(bg, "default", tk.ID); got.Status != StatusReview || got.Verified {
		t.Fatalf("the task moved: %+v", got)
	}
	v := Actor{Kind: Agent, Name: "v1-t1", Session: se.ID, Task: tk.ID, Verifier: true}
	if got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "ok", v); err != nil || !got.Verified {
		t.Fatalf("the bound verifier: %+v %v", got, err)
	}
}

// A session nobody manages keeps its rule: another named agent verifies.
func TestUnboundAgentStillVerifiesAnUnmanagedSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := reviewTask(t, s)
	if got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "ok", bot("checker")); err != nil || !got.Verified {
		t.Fatalf("verify: %+v %v", got, err)
	}
}
