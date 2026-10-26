package tasks

import (
	"errors"
	"testing"
)

// reviewTask makes a running session whose one task a worker handed to review.
func reviewTask(t *testing.T, s *Store) (Session, Task) {
	t.Helper()
	se := mustSession(t, s, "default", "ver")
	tk := mustTask(t, s, "default", se.ID, "work")
	other := mustTask(t, s, "default", se.ID, "other")
	_ = other
	mustRun(t, s, "default", se)
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, bot("m1-t1"), 0); err != nil {
		t.Fatal(err)
	}
	finishTask(t, s, se.ID, tk.ID, "m1-t1")
	return se, tk
}

func TestVerifierBindingMayOnlyVerifyItsTask(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := reviewTask(t, s)
	other, _ := s.SessionNext(bg, "default", se.ID, bot("peek"), false, 0)
	if other == nil {
		t.Fatal("no other ready task")
	}
	v := Actor{Kind: Agent, Name: "v1-t1", Session: se.ID, Task: tk.ID, Verifier: true}

	refused := map[string]error{}
	_, refused["claim own"] = s.SessionClaim(bg, "default", se.ID, tk.ID, v, 0)
	_, refused["claim other"] = s.SessionClaim(bg, "default", se.ID, other.ID, v, 0)
	_, refused["next"] = s.SessionNext(bg, "default", se.ID, v, true, 0)
	_, refused["finish"] = s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "x"}, v)
	_, refused["release"] = s.SessionRelease(bg, "default", se.ID, tk.ID, "x", v)
	_, refused["split"] = s.SplitTask(bg, "default", se.ID, other.ID, []SplitPart{{Title: "a", Acceptance: "x"}, {Title: "b", Acceptance: "y"}}, v)
	_, refused["verify other"] = s.VerifySessionTask(bg, "default", se.ID, other.ID, true, "", v)
	_, refused["comment other"] = s.SessionComment(bg, "default", se.ID, other.ID, "hi", v)
	_, refused["add"] = s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "n", Acceptance: "x"}, v)
	refused["event"] = s.AddSessionEvent(bg, "default", se.ID, EventNote, "x", v)
	for what, err := range refused {
		if !errors.Is(err, ErrOperatorOnly) && !errors.Is(err, ErrNotClaimant) && !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: want a refusal, got %v", what, err)
		}
	}
	// state: nothing but the review task moved; it is still in review and unverified
	if got, _, _ := s.Get(bg, "default", tk.ID); got.Status != StatusReview || got.Verified || got.Claim != nil {
		t.Fatalf("task moved: %+v", got)
	}
	if got, _, _ := s.Get(bg, "default", other.ID); got.Status != StatusReady || got.Claim != nil {
		t.Fatalf("other moved: %+v", got)
	}
	// and what it may do
	if _, err := s.SessionComment(bg, "default", se.ID, tk.ID, "ran the tests", v); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "test", Ref: "go test ./... ok"}, v); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "all criteria met", v)
	if err != nil || !got.Verified {
		t.Fatalf("verify: %+v %v", got, err)
	}
}

func TestVerifierWithoutATaskIsRefused(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := reviewTask(t, s)
	v := Actor{Kind: Agent, Name: "v1-t1", Session: se.ID, Verifier: true}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("verify by a task-less verifier: %v", err)
	}
	if _, err := s.SessionComment(bg, "default", se.ID, tk.ID, "x", v); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("comment by a task-less verifier: %v", err)
	}
	if got, _, _ := s.Get(bg, "default", tk.ID); got.Verified {
		t.Fatal("verified")
	}
}

func TestVerifierCannotCommentOnATaskNotInReview(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "ver")
	tk := mustTask(t, s, "default", se.ID, "work")
	mustRun(t, s, "default", se)
	v := Actor{Kind: Agent, Name: "v1-t1", Session: se.ID, Task: tk.ID, Verifier: true}
	if _, err := s.SessionComment(bg, "default", se.ID, tk.ID, "x", v); !errors.Is(err, ErrNotClaimant) {
		t.Fatalf("comment on a Ready task: %v", err)
	}
	if d, _ := s.SessionTask(bg, "default", se.ID, tk.ID, human); len(d.Events) != 2 {
		t.Fatalf("events written: %+v", d.Events)
	}
}
