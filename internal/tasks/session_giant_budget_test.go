package tasks

import (
	"errors"
	"strings"
	"testing"
)

// With no budget anywhere (the root has none) the operator's ceiling is what the guard measures by:
// a child that brings no budget is giant against it, so the person decides. Without a ceiling a
// lead does not approve at all (see TestALeadWithoutACeilingAboveZeroNeverApproves).
func TestAChildWithNoBudgetUnderAnUnbudgetedMissionIsGiant(t *testing.T) {
	a := newApprovalTree(t)
	for _, id := range []int64{a.r.ID, a.c.ID} {
		if _, err := a.db.Exec(`UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	_, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	var g *GiantError
	if !errors.As(err, &g) || !strings.Contains(strings.Join(g.Reasons, ";"), "ceiling") {
		t.Fatalf("a lead approved unlimited spend: %v", err)
	}
	if se := a.state(t, a.c.ID); se.State != SessionDraft {
		t.Fatalf("%+v", se)
	}
	// With no ceiling the lead is not allowed to approve anything, giant or not.
	a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: true, WorkingDepth: 3}
	a.refused(t, a.c.ID, leadOf("conv-p"), "no budget and no ceiling")
}

// The share is the child's budget against its PARENT's. The budgets of the parent's other children are
// not counted: each session is judged by itself.
func TestTheShareIsOfTheParentsBudgetAndSiblingsAreNotCounted(t *testing.T) {
	a := newApprovalTree(t)
	// P holds $30; C holds $10. A second child of P at $12 (40%) is within; $12.01 is not.
	mk := func(usd float64) Session {
		se, err := a.s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: a.p.ID, Kind: SessionKindTask, BudgetUSD: usd, MaxTasks: 5}, human)
		if err != nil {
			t.Fatal(err)
		}
		linkConv(t, a.db, se.ID, "conv-kid")
		mustTask(t, a.s, "default", se.ID, "work")
		return se
	}
	facts := func(id int64) GiantFacts {
		f, err := a.s.GiantFactsOf(bg, "default", id, human)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	within, over := mk(12), mk(12.01)
	if r := facts(within.ID).Reasons; len(r) != 0 {
		t.Fatalf("$12 of $30 is within the share, whatever the siblings hold: %v", r)
	}
	if f := facts(over.ID); len(f.Reasons) != 1 || !strings.Contains(f.Reasons[0], "40%") || f.BudgetShare < 0.4 {
		t.Fatalf("$12.01 of $30 is over it: %+v", f)
	}
	// An approved sibling changes nothing for the next one.
	if _, err := a.s.ApproveSession(bg, "default", a.c.ID, human); err != nil {
		t.Fatal(err)
	}
	if r := facts(within.ID).Reasons; len(r) != 0 {
		t.Fatalf("a running sibling must not count: %v", r)
	}
}
