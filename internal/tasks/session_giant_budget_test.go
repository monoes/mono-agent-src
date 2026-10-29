package tasks

import (
	"errors"
	"strings"
	"testing"
)

// With no budget anywhere (the root has none and the operator set no ceiling) the guard has
// nothing to measure, so a child that brings no budget either is giant: the person decides.
func TestAChildWithNoBudgetUnderAnUnbudgetedMissionIsGiant(t *testing.T) {
	a := newApprovalTree(t)
	for _, id := range []int64{a.r.ID, a.c.ID} {
		if _, err := a.db.Exec(`UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	_, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	var g *GiantError
	if !errors.As(err, &g) || !strings.Contains(strings.Join(g.Reasons, ";"), "no budget set: the person decides") {
		t.Fatalf("a lead approved unlimited spend: %v", err)
	}
	if se := a.state(t, a.c.ID); se.State != SessionDraft {
		t.Fatalf("%+v", se)
	}
	// The operator's ceiling is a budget to measure by: the same child is then giant for the old reason, not this one.
	a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: true, WorkingDepth: 3, CeilingUSD: 50}
	_, err = a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	if !errors.As(err, &g) || strings.Contains(strings.Join(g.Reasons, ";"), "no budget set") {
		t.Fatalf("with a ceiling the reason is the ceiling's: %v", err)
	}
	// A child that brings a budget under an unbudgeted mission and no ceiling is not blocked by this rule.
	if _, err := a.db.Exec(`UPDATE task_sessions SET budget_usd = 3 WHERE id = ?`, a.c.ID); err != nil {
		t.Fatal(err)
	}
	a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: true, WorkingDepth: 3}
	if se, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p")); err != nil || se.State != SessionRunning {
		t.Fatalf("a child with its own budget: %+v %v", se, err)
	}
}

// The 40% rule counts what the parent's approved siblings already hold, so siblings that each
// take 40% cannot together exceed the root.
func TestTheShareCountsTheBudgetsOfApprovedSiblings(t *testing.T) {
	a := newApprovalTree(t)
	facts := func(id int64) GiantFacts {
		f, err := a.s.GiantFactsOf(bg, "default", id, human)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if r := facts(a.p.ID).Reasons; len(r) != 0 {
		t.Fatalf("$30 of $100 is within the share: %v", r)
	}
	// The sibling is approved and runs with $30 of the root's $100.
	mustTask(t, a.s, "default", a.sib.ID, "work")
	if _, err := a.s.ApproveSession(bg, "default", a.sib.ID, human); err != nil {
		t.Fatal(err)
	}
	if r := facts(a.p.ID).Reasons; len(r) != 1 || !strings.Contains(r[0], "40%") {
		t.Fatalf("$30 of the $70 that is left is over 40%%: %v", r)
	}
	_, err := a.s.ApproveSession(bg, "default", a.p.ID, leadOf("conv-root"))
	var g *GiantError
	if !errors.As(err, &g) {
		t.Fatalf("a lead approved the sibling that took the root over its share: %v", err)
	}
	// A sibling that is still a draft holds nothing.
	a2 := newApprovalTree(t)
	if f, err := a2.s.GiantFactsOf(bg, "default", a2.p.ID, human); err != nil || len(f.Reasons) != 0 {
		t.Fatalf("a draft sibling counts: %+v %v", f, err)
	}
}
