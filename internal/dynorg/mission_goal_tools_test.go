package dynorg

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

func goalCriteriaArg() map[string]any {
	return map[string]any{"criteria": []map[string]string{
		{"kind": "command", "spec": "go test ./...", "expected": "exit:0"},
		{"kind": "rubric", "spec": "The person is satisfied with the result"},
	}}
}

// goalMission is a mission lead in a chat of the given mode with a planned session. withGoals false leaves the goal store out.
func goalMission(t *testing.T, mode string, withGoals bool) (*Conductor, *MissionConfig, *sql.DB, int64) {
	t.Helper()
	db := openMissionDB(t)
	if mode != "" {
		if _, err := db.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, chat_mode, created_at, updated_at) VALUES (?, 'conv-1', ?, 'x', 'x')`, missionProfile, mode); err != nil {
			t.Fatal(err)
		}
	}
	c, _, m := missionConductor(t, tasks.NewStore(db), "conv-1", true, nil)
	if withGoals {
		m.Goals = missionpursuit.NewStore(db)
	}
	res, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatal(err)
	}
	return c, m, db, int64(res["session_id"].(float64))
}

func specNames(m *MissionConfig) map[string]bool {
	names := map[string]bool{}
	for _, s := range MissionPlanToolSpecs(m) {
		names[s.Name] = true
	}
	return names
}

func TestMissionGoalToolsAreListedOnlyInAMissionChat(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		withGoals bool
		want      bool
	}{{"mission", true, true}, {"off", true, false}, {"open", true, false}, {"", true, false}, {"mission", false, false}} {
		_, m, _, _ := goalMission(t, tc.mode, tc.withGoals)
		names := specNames(m)
		if got := names[ToolMissionGoalPropose] && names[ToolMissionGoalStatus]; got != tc.want {
			t.Errorf("mode %q goals %t: the goal tools are listed = %t, want %t", tc.mode, tc.withGoals, got, tc.want)
		}
		if names[ToolMissionGoalPropose] != names[ToolMissionGoalStatus] {
			t.Errorf("mode %q: only one of the two goal tools is listed", tc.mode)
		}
	}
}

func TestMissionLeadCanOnlyProposeTheGoal(t *testing.T) {
	c, m, db, session := goalMission(t, "mission", true)
	res, err := call(t, c, ToolMissionGoalPropose, goalCriteriaArg())
	if err != nil {
		t.Fatal(err)
	}
	if res["state"] != "proposed" || res["hash"] == "" || res["confirmed_by"] != nil {
		t.Fatalf("propose: %v", res)
	}
	g, err := m.Goals.Get(context.Background(), missionProfile, session)
	if err != nil || g.State != "proposed" || g.ProposedBy != "mission-lead" || g.ConfirmedBy != "" {
		t.Fatalf("stored goal %+v %v", g, err)
	}
	st, err := call(t, c, ToolMissionGoalStatus, map[string]any{})
	if err != nil || st["state"] != "proposed" || st["hash"] != g.Hash {
		t.Fatalf("status: %v %v", st, err)
	}
	// the lead has no tool to set or confirm, under any name, and no tool of the list is one
	for name := range specNames(m) {
		if strings.Contains(name, "goal") && name != ToolMissionGoalPropose && name != ToolMissionGoalStatus {
			t.Errorf("the lead is offered %s", name)
		}
	}
	for _, name := range []string{"mission_goal_set", "mission_goal_confirm", "mission_goal_lock"} {
		if _, err := call(t, c, name, goalCriteriaArg()); err == nil {
			t.Errorf("%s is a tool", name)
		}
	}
	// the person confirms; the lead's next proposal is refused: a changed goal means a new mission
	if _, err := m.Goals.Confirm(context.Background(), missionProfile, session, g.Hash, missionHuman); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, ToolMissionGoalPropose, goalCriteriaArg()); err == nil || !strings.Contains(err.Error(), "new mission") {
		t.Errorf("a proposal over a confirmed goal: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_goals WHERE state <> 'confirmed'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d goals are not the confirmed one", n)
	}
}

func TestMissionGoalProposeRefusesBadCriteriaAndAChatThatLeftMissionMode(t *testing.T) {
	c, _, db, _ := goalMission(t, "mission", true)
	if _, err := call(t, c, ToolMissionGoalPropose, map[string]any{"criteria": []map[string]string{{"kind": "command", "spec": "true"}}}); err == nil || !strings.Contains(err.Error(), "rubric") {
		t.Errorf("criteria with no rubric: %v", err)
	}
	if _, err := call(t, c, ToolMissionGoalPropose, map[string]any{"criteria": "not a list"}); err == nil {
		t.Error("bad arguments were accepted")
	}
	if _, err := db.Exec(`UPDATE coder_sessions SET chat_mode = 'off'`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, ToolMissionGoalPropose, goalCriteriaArg()); err == nil {
		t.Error("a proposal was stored for a chat that is not in mission mode")
	}
}

func TestMissionGoalToolsAreMissionPlanTools(t *testing.T) {
	for _, n := range []string{ToolMissionGoalPropose, ToolMissionGoalStatus} {
		if !IsMissionPlanTool(n) {
			t.Errorf("%s is not a mission plan tool", n)
		}
	}
}

// Only "no goal yet" reads as state none; any other failure is an error the lead sees.
func TestMissionGoalStatusOnlyMapsNotFoundToNone(t *testing.T) {
	c, _, db, _ := goalMission(t, "mission", true)
	res, err := call(t, c, ToolMissionGoalStatus, map[string]any{})
	if err != nil || res["state"] != "none" {
		t.Fatalf("no goal yet: %v %v", res, err)
	}
	if _, err := call(t, c, ToolMissionGoalPropose, goalCriteriaArg()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE mission_goal_criteria SET spec = 'tampered' WHERE pos = 1`); err != nil {
		t.Fatal(err)
	}
	if res, err := call(t, c, ToolMissionGoalStatus, map[string]any{}); err == nil {
		t.Errorf("a damaged goal answered %v instead of an error", res)
	}
}
