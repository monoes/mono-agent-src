package tasks

import (
	"errors"
	"testing"
)

func TestGiantFactsExplainWhyAndAreTheOperatorsOnly(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.SetApprovalSource(&fakeApproval{pol: map[string]ApprovalPolicy{"conv-root": {WorkingDepth: 4}}, raised: map[int64]string{}})
	root, err := s.CreateSession(bg, "default", SessionInput{Title: "root", ConversationID: "conv-root", BudgetUSD: 100}, human)
	if err != nil {
		t.Fatal(err)
	}
	kid, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: root.ID, BudgetUSD: 50, MaxTasks: 30}, human)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.GiantFactsOf(bg, "default", kid.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Reasons) != 2 || f.BudgetShare != 0.5 || f.BudgetUSD != 50 {
		t.Fatalf("facts = %+v", f)
	}
	if rf, err := s.GiantFactsOf(bg, "default", root.ID, human); err != nil || len(rf.Reasons) != 0 {
		t.Fatalf("a root is never giant: %+v %v", rf, err)
	}
	if _, err := s.GiantFactsOf(bg, "default", kid.ID, Actor{Kind: Agent, Name: "bot"}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent must be refused: %v", err)
	}
}
