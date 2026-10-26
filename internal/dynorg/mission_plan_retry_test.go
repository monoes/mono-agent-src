package dynorg

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A retried mission_plan (same conversation, same round, same plan) returns the draft it
// made: no second session, no second set of tasks, no error. A different plan is still refused.
func TestMissionPlanRetryReturnsTheSameDraft(t *testing.T) {
	st := missionStore(t)
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	first, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatal(err)
	}
	again, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatalf("a retried mission_plan: %v", err)
	}
	if again["session_id"] != first["session_id"] || again["deduped"] != true || again["task_count"] != first["task_count"] {
		t.Fatalf("retry %v, first %v", again, first)
	}
	list, _ := st.ListSessions(context.Background(), missionProfile, tasks.SessionFilter{}, missionHuman)
	if len(list) != 1 {
		t.Fatalf("%d sessions", len(list))
	}
	plan, _ := st.PlanOfSession(context.Background(), missionProfile, list[0].ID, missionHuman)
	if len(plan.Tasks) != 3 {
		t.Fatalf("%d tasks after a retry", len(plan.Tasks))
	}
	// A new round (the first session closed) is a new key: the same plan starts a second session.
	if _, err := st.StopSession(context.Background(), missionProfile, list[0].ID, missionHuman); err != nil {
		t.Fatal(err)
	}
	next, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil || next["session_id"] == first["session_id"] || next["deduped"] != nil {
		t.Fatalf("plan after stop: %v %v", next, err)
	}
	// Another conversation planning the same thing gets its own session.
	other, _, _ := missionConductor(t, st, "conv-2", true, nil)
	o, err := call(t, other, ToolMissionPlan, goodPlan())
	if err != nil || o["session_id"] == next["session_id"] {
		t.Fatalf("other conversation: %v %v", o, err)
	}
}
