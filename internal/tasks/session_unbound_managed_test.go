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

// Pausing or stopping a session a conversation manages is the operator's and the lead's (bound to
// the session); an agent with no binding is refused, and an unmanaged session keeps its rule.
func TestUnboundAgentCannotPauseOrStopAManagedSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "managed", Goal: "g", ConversationID: "conv-1"}, human)
	if err != nil {
		t.Fatal(err)
	}
	mustTask(t, s, "default", se.ID, "a")
	mustRun(t, s, "default", se)
	stranger := bot("stranger")
	if _, err := s.PauseSession(bg, "default", se.ID, stranger); !errors.Is(err, ErrOperatorOnly) {
		t.Errorf("pause by an unbound agent: %v", err)
	}
	if _, err := s.StopSession(bg, "default", se.ID, stranger); !errors.Is(err, ErrOperatorOnly) {
		t.Errorf("stop by an unbound agent: %v", err)
	}
	// The lead and the conductor act in process under an actor bound to the session (Session set,
	// no task): that is what lets them, and it is what an unbound agent lacks.
	lead := Actor{Kind: Agent, Name: "lead", Session: se.ID, Conversation: "conv-1"}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "from the lead", Acceptance: "done"}, lead); err != nil {
		t.Errorf("the lead adds a task to its session: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "from a stranger", Acceptance: "done"}, stranger); !errors.Is(err, ErrOperatorOnly) {
		t.Errorf("an unbound agent adds a task: %v", err)
	}
	if _, err := s.PauseSession(bg, "default", se.ID, lead); err != nil {
		t.Errorf("the lead pauses its session: %v", err)
	}
	if got, err := s.StopSession(bg, "default", se.ID, human); err != nil || got.State != SessionStopped {
		t.Errorf("the operator stops it: %+v %v", got, err)
	}
	free, err := s.CreateSession(bg, "default", SessionInput{Title: "free", Goal: "g"}, human)
	if err != nil {
		t.Fatal(err)
	}
	mustTask(t, s, "default", free.ID, "a")
	mustRun(t, s, "default", free)
	if _, err := s.StopSession(bg, "default", free.ID, stranger); err != nil {
		t.Errorf("an agent stops an unmanaged session: %v", err)
	}
}
