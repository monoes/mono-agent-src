package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The lead's tools for the ultimate goal of its mission (T6). The lead only PROPOSES the criteria: there is no tool to set,
// confirm or lock them, those are the person's (task session goal set|confirm). The tools are listed only in a chat
// whose mode is mission.
const (
	ToolMissionGoalPropose = "mission_goal_propose"
	ToolMissionGoalStatus  = "mission_goal_status"
)

func goalCriterionSchema() map[string]any {
	return map[string]any{"type": "array", "description": "The criteria of the ultimate goal, 1 to 16, in order.", "items": obj(map[string]any{
		"kind":     str("command, file, output (a machine check) or rubric (the person decides). At least one criterion is a rubric: the person is always the final judge."),
		"spec":     str("command/output: the command line (no shell operators). file: a relative path. rubric: what the person judges, in prose."),
		"expected": str("command: exit:N[,M] or the exact stdout. output: the exact stdout. file: exists, absent, sha256:<64 hex> or size<=N. rubric: optional."),
	}, "kind", "spec")}
}

// missionGoalToolSpecs are the goal tools for this conversation, none unless the chat is in mission mode (an unreadable mode lists none).
func missionGoalToolSpecs(m *MissionConfig) []monomind.ToolSpec {
	if m.Goals == nil || !m.Goals.ChatIsMission(context.Background(), m.ProfileID, m.ConversationID) {
		return nil
	}
	return []monomind.ToolSpec{
		{Name: ToolMissionGoalPropose,
			Description: "Propose the criteria of the mission's ultimate goal: what must be true for the whole mission to be done. It stays a proposal, which you may revise by proposing again, until the person confirms it; you cannot confirm it. Once confirmed the goal is final: a changed goal means a new mission. Returns the proposal with its hash.",
			Schema:      obj(map[string]any{"criteria": goalCriterionSchema()}, "criteria")},
		{Name: ToolMissionGoalStatus,
			Description: "Read the ultimate goal as it stands: its state (proposed, confirmed or locked), its criteria and their hash, and how its checks stand once the host runs them: the latest check's outcome, which machine criteria pass, how many of the person's criteria they have answered yes to, and whether the goal is satisfied. Ids, enums and counts only. Takes no arguments.",
			Schema:      obj(map[string]any{})},
	}
}

// goalStatusView is the goal and, beside it, how its checks stand: ids, enums and counts the host wrote (never a program's output,
// the checker's words or the person's notes).
type goalStatusView struct {
	missionpursuit.Goal
	Checks     *missionpursuit.GoalChecks `json:"checks,omitempty"`
	ChecksNote string                     `json:"checks_note,omitempty"`
}

func (c *Conductor) missionGoal(ctx context.Context, m *MissionConfig, name string, args json.RawMessage) (string, error) {
	if m.Goals == nil {
		return "", fmt.Errorf("the ultimate goal is not offered in this conversation")
	}
	se, err := m.mustActive(ctx)
	if err != nil {
		return "", err
	}
	if name == ToolMissionGoalStatus {
		g, err := m.Goals.Get(ctx, m.ProfileID, se.ID)
		if err != nil {
			if !errors.Is(err, tasks.ErrNotFound) {
				return "", err
			}
			return marshal(map[string]any{"state": "none", "next": "No goal is proposed yet: propose its criteria with mission_goal_propose."})
		}
		gc, err := m.Goals.GoalChecks(ctx, m.ProfileID, se.ID)
		if err != nil {
			// the goal is still shown: the checks are unknown, said in fixed words (the close is held back meanwhile: CloseRefusal fails closed)
			return marshal(goalStatusView{Goal: g, ChecksNote: "the checks of this goal could not be read just now; treat them as unknown"})
		}
		return marshal(goalStatusView{Goal: g, Checks: &gc})
	}
	var a struct {
		Criteria []missionpursuit.Criterion `json:"criteria"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	g, err := m.Goals.Propose(ctx, m.ProfileID, se.ID, a.Criteria, m.actor(se.ID))
	if err != nil {
		return "", err
	}
	return marshal(g)
}
