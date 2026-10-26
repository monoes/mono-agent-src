package tasks

import (
	"errors"
	"testing"
)

// In a session a conversation manages, an agent with no token binding (no session, no task) may
// read and comment but not change the work: claim, take the next task, finish, release, split,
// add, attach. A bound agent and the operator still do, and so does a session nobody manages.
func TestUnboundAgentCannotChangeWorkInAManagedSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "managed", Goal: "g", ConversationID: "conv-1"}, human)
	if err != nil {
		t.Fatal(err)
	}
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b")
	c := mustTask(t, s, "default", se.ID, "c")
	mustRun(t, s, "default", se)
	own := Actor{Kind: Agent, Name: "m1-t1", Session: se.ID, Task: a.ID}
	if _, err := s.SessionClaim(bg, "default", se.ID, a.ID, own, 0); err != nil {
		t.Fatalf("a bound worker claims its task: %v", err)
	}
	stranger := bot("stranger")
	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%s by an unbound agent: want a refusal, got %v", what, err)
		}
	}
	_, err = s.SessionClaim(bg, "default", se.ID, b.ID, stranger, 0)
	refused("claim", err)
	_, err = s.SessionNext(bg, "default", se.ID, stranger, true, 0)
	refused("next --claim", err)
	_, err = s.SessionFinish(bg, "default", se.ID, a.ID, Outcome{Result: "x"}, stranger)
	refused("finish", err)
	_, err = s.SessionRelease(bg, "default", se.ID, a.ID, "no", stranger)
	refused("release", err)
	_, err = s.SplitTask(bg, "default", se.ID, c.ID, []SplitPart{{Title: "x", Acceptance: "x"}, {Title: "y", Acceptance: "y"}}, stranger)
	refused("split", err)
	_, err = s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "d", Acceptance: "ok"}, stranger)
	refused("add", err)
	_, err = s.AddDependencies(bg, "default", se.ID, c.ID, []int64{b.ID}, stranger)
	refused("add dependencies", err)
	_, err = s.AddArtifact(bg, "default", se.ID, a.ID, ArtifactInput{Kind: "commit", Ref: "abc1234"}, stranger)
	refused("artifact", err)

	// State: nothing moved.
	if got, _, _ := s.Get(bg, "default", b.ID); got.Claim != nil {
		t.Errorf("an unbound agent holds a claim: %+v", got.Claim)
	}
	if got, _, _ := s.Get(bg, "default", a.ID); got.Status != StatusInProgress {
		t.Errorf("task a moved: %s", got.Status)
	}
	// Reading stays open.
	if _, err := s.SessionTask(bg, "default", se.ID, a.ID, stranger); err != nil {
		t.Errorf("read: %v", err)
	}
	// (A comment stays governed by the holder rule that was there before: the gate adds nothing to it.)
	// The bound worker and the operator are not affected.
	if _, err := s.SessionFinish(bg, "default", se.ID, a.ID, Outcome{Result: "done"}, own); err != nil {
		t.Errorf("the bound worker finishes: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "e", Acceptance: "ok"}, human); err != nil {
		t.Errorf("the operator adds: %v", err)
	}
	lead := Actor{Kind: Agent, Name: "mission-lead", Session: se.ID}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "f", Acceptance: "ok"}, lead); err != nil {
		t.Errorf("the conversation's lead adds: %v", err)
	}
}

// A session nobody manages keeps today's rule: an unbound named agent works it.
func TestUnboundAgentStillWorksAnUnmanagedSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "mine", Goal: "g"}, human)
	if err != nil {
		t.Fatal(err)
	}
	a := mustTask(t, s, "default", se.ID, "a")
	mustRun(t, s, "default", se)
	if _, err := s.SessionClaim(bg, "default", se.ID, a.ID, bot("anyone"), 0); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "d", Acceptance: "ok"}, bot("anyone")); err != nil {
		t.Fatalf("add: %v", err)
	}
}
