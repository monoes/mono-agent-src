package tasks

import (
	"errors"
	"strings"
	"testing"
)

// What an agent is told about giant work is that the person decides, not the figures behind it:
// the mission's remaining budget, the ceiling and the thresholds stay in the operator's view.
func TestTheGiantRefusalTellsTheAgentNothingAboutWhy(t *testing.T) {
	a := newApprovalTree(t)
	if _, err := a.db.Exec(`UPDATE task_sessions SET spent_usd = 50 WHERE id = ?`, a.r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.UpdateSession(bg, "default", a.c.ID, SessionUpdate{BudgetUSD: ptr(25.0)}, human); err != nil {
		t.Fatal(err)
	}
	a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: true, WorkingDepth: 3, CeilingUSD: 40}
	_, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	var g *GiantError
	if !errors.As(err, &g) || len(g.Reasons) == 0 {
		t.Fatalf("want a giant refusal: %v", err)
	}
	msg := err.Error()
	for _, leak := range []string{"$", "%", "left", "ceiling", "40", "25", "50", "budget", "access", "depth", "tasks"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the refusal tells the agent %q: %s", leak, msg)
		}
	}
	if !strings.Contains(msg, "needs the person") || !strings.Contains(msg, "#") {
		t.Errorf("the refusal must say the person decides: %s", msg)
	}
	// The operator's view keeps the reasons.
	if f, err := a.s.GiantFactsOf(bg, "default", a.c.ID, human); err != nil || len(f.Reasons) == 0 {
		t.Fatalf("the operator's facts: %+v %v", f, err)
	}
}
