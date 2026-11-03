package tasks

import (
	"errors"
	"testing"
)

func bossFor(root Session) Actor {
	return Actor{Kind: Agent, Name: "lead-root", Session: root.ID, Conversation: "conv-root"}
}

func TestPlanFactsForBossAreReadPessimisticallyAndOnlyForTheParentsLead(t *testing.T) {
	s, kid := guardedDraft(t)
	boss := bossFor(Session{ID: kid.ParentSessionID})
	f, err := s.PlanFactsForBoss(bg, "default", kid.ID, boss)
	if err != nil || !f.Open || f.Tasks != 30 || f.SpendMicro != 5_000_000 || f.Rev == 0 || f.Digest == "" || f.Depth < 1 {
		t.Fatalf("%+v %v", f, err)
	}
	for name, a := range map[string]Actor{
		"operator":          human,
		"another chat":      {Kind: Agent, Name: "x", Conversation: "conv-other"},
		"no conversation":   {Kind: Agent, Name: "x"},
		"the session's own": {Kind: Agent, Name: "x", Conversation: "conv-kid"},
		"bound to a task":   {Kind: Agent, Name: "lead-root", Task: 1, Conversation: "conv-root"},
		"a verifier":        {Kind: Agent, Name: "lead-root", Verifier: true, Conversation: "conv-root"},
	} {
		if _, err := s.PlanFactsForBoss(bg, "default", kid.ID, a); err == nil {
			t.Fatalf("%s read the plan facts", name)
		}
	}
	if _, err := s.db.ExecContext(bg, `UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, kid.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.PlanFactsForBoss(bg, "default", kid.ID, boss); f.SpendMicro <= 5_000_000_000_000 {
		t.Fatalf("a plan with no budget is counted at %d", f.SpendMicro)
	}
}

func TestApprovePlanAsBossNeedsTheRevisionAndTheDigestAndTheParentsLead(t *testing.T) {
	s, kid := guardedDraft(t)
	boss := bossFor(Session{ID: kid.ParentSessionID})
	f, _ := s.PlanFactsForBoss(bg, "default", kid.ID, boss)
	for name, g := range map[string]PlanGuard{
		"no guard":       {},
		"no digest":      {Rev: f.Rev},
		"no revision":    {Digest: f.Digest},
		"another digest": {Rev: f.Rev, Digest: "00"},
		"old revision":   {Rev: f.Rev - 1, Digest: f.Digest},
	} {
		if _, err := s.ApprovePlanAsBoss(bg, "default", kid.ID, boss, g); !errors.Is(err, ErrPlanChanged) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := s.ApprovePlanAsBoss(bg, "default", kid.ID, human, PlanGuard{Rev: f.Rev, Digest: f.Digest}); err == nil {
		t.Fatal("the operator's actor approved as the boss")
	}
	if _, err := s.ApprovePlanAsBoss(bg, "default", kid.ID, Actor{Kind: Agent, Name: "x", Conversation: "conv-other"}, PlanGuard{Rev: f.Rev, Digest: f.Digest}); err == nil {
		t.Fatal("another chat approved as the boss")
	}
	if se, _ := s.GetSession(bg, "default", kid.ID, human); se.State != SessionDraft {
		t.Fatalf("a refusal approved: %+v", se)
	}
	se, err := s.ApprovePlanAsBoss(bg, "default", kid.ID, boss, PlanGuard{Rev: f.Rev, Digest: f.Digest})
	if err != nil || se.State != SessionRunning || se.ApprovalBasis != ApprovalBoss {
		t.Fatalf("%+v %v", se, err)
	}
	if f2, _ := s.PlanFactsForBoss(bg, "default", kid.ID, boss); f2.Open {
		t.Fatal("an approved plan is still open")
	}
}
