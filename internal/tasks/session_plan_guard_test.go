package tasks

import (
	"errors"
	"testing"
)

func guardedDraft(t *testing.T) (*Store, Session) {
	t.Helper()
	s, _, _ := newTestStore(t)
	root, err := s.CreateSession(bg, "default", SessionInput{Title: "root", ConversationID: "conv-root", BudgetUSD: 100}, human)
	if err != nil {
		t.Fatal(err)
	}
	kid, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", Goal: "g", ParentSessionID: root.ID, BudgetUSD: 5, MaxTasks: 30}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionTask(bg, "default", kid.ID, SessionTaskInput{Title: "one", Acceptance: "a"}, human); err != nil {
		t.Fatal(err)
	}
	return s, kid
}

func TestApproveSessionAtChecksTheRevisionAndTheDigest(t *testing.T) {
	s, kid := guardedDraft(t)
	v, err := s.PlanOf(bg, "default", kid.ID, human)
	if err != nil || v.Rev == 0 || v.Digest == "" || v.TaskCount != 1 {
		t.Fatalf("view = %+v, %v", v, err)
	}
	if _, err := s.PlanOf(bg, "default", kid.ID, Actor{Kind: Agent, Name: "bot"}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent reads no digest: %v", err)
	}
	if _, err := s.ApproveSessionAt(bg, "default", kid.ID, Actor{Kind: Agent, Name: "bot"}, PlanGuard{Rev: v.Rev}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent never approves as the person: %v", err)
	}
	// the right revision with a digest of another plan is refused
	if _, err := s.ApproveSessionAt(bg, "default", kid.ID, human, PlanGuard{Rev: v.Rev, Digest: "00"}); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("a digest that differs must refuse: %v", err)
	}
	if _, err := s.ApproveSessionAt(bg, "default", kid.ID, human, PlanGuard{Rev: v.Rev - 1, Digest: v.Digest}); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("a revision that differs must refuse: %v", err)
	}
	if se, _ := s.GetSession(bg, "default", kid.ID, human); se.State != SessionDraft {
		t.Fatalf("a refusal approved: %+v", se)
	}
	if se, err := s.ApproveSessionAt(bg, "default", kid.ID, human, PlanGuard{Rev: v.Rev, Digest: v.Digest}); err != nil || se.State != SessionRunning {
		t.Fatalf("the plan as read: %+v %v", se, err)
	}
}

func TestPlanDigestMovesWithTheWordsOfAnyTask(t *testing.T) {
	s, kid := guardedDraft(t)
	before, _ := s.PlanOf(bg, "default", kid.ID, human)
	if _, err := s.AddSessionTask(bg, "default", kid.ID, SessionTaskInput{Title: "two", Acceptance: "b"}, human); err != nil {
		t.Fatal(err)
	}
	after, _ := s.PlanOf(bg, "default", kid.ID, human)
	if after.Digest == before.Digest || after.TaskCount != 2 || after.Rev == before.Rev {
		t.Fatalf("before %+v after %+v", before, after)
	}
}
