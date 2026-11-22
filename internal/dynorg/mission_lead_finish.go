package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// ToolMissionTaskFinish hands back a task the scheduler gave to the lead itself. The lead's shell carries
// no task token (only a worker's does), so the CLI's `task finish --as lead` is refused in a session a
// conversation manages; the conductor, which holds the lead's claim, does it from this tool instead.
const ToolMissionTaskFinish = "mission_task_finish"

func missionTaskFinishSpec() monomind.ToolSpec {
	return monomind.ToolSpec{Name: ToolMissionTaskFinish,
		Description: "Hand back a task you did yourself (one mission_next or mission_assign gave to the lead): attach the evidence, then send the task to review with your result, or with a question if you cannot go on. Give a commit (its full object id), a diff, test output or a file as evidence for a code task. Only a task you hold; a task of a worker is finished by its worker.",
		Schema: obj(map[string]any{
			"task_id":  intProp("The task you hold."),
			"result":   str("What you did and how you checked it (the command you ran and its result). Exactly one of result and question."),
			"question": str("What you need answered to go on."),
			"evidence": map[string]any{"type": "array", "description": "Optional evidence to attach first.", "items": obj(map[string]any{
				"kind": str("commit, file, test, diff, log or report."), "ref": str("The commit id, path or command."), "note": str("Optional note."),
			}, "kind", "ref")},
		}, "task_id")}
}

// handleMissionTaskFinish finishes a task the lead holds, as the lead.
func (c *Conductor) handleMissionTaskFinish(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		TaskID   int64  `json:"task_id"`
		Result   string `json:"result"`
		Question string `json:"question"`
		Evidence []struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
			Note string `json:"note"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	mc, err := c.missions()
	if err != nil {
		return "", err
	}
	sid, err := c.sessionOf(ctx, mc, 0)
	if err != nil {
		return "", err
	}
	if (strings.TrimSpace(a.Result) == "") == (strings.TrimSpace(a.Question) == "") {
		return "", errors.New("give exactly one of result and question")
	}
	t, _, err := mc.Store.Get(ctx, mc.ProfileID, a.TaskID)
	if err != nil {
		return "", err
	}
	if t.SessionID != sid {
		return "", fmt.Errorf("task #%d is not a task of this mission", a.TaskID)
	}
	if t.Status != tasks.StatusInProgress || t.Claim == nil || t.Claim.By != missionLeadName {
		return "", fmt.Errorf("task #%d is not held by the lead: only a task mission_next or mission_assign gave to the lead is finished here", a.TaskID)
	}
	actor := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: sid, Task: t.ID}
	for _, e := range a.Evidence {
		if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, sid, t.ID, tasks.ArtifactInput{Kind: e.Kind, Ref: e.Ref, Note: e.Note}, actor); err != nil {
			return "", fmt.Errorf("evidence %s %q: %w", e.Kind, boundText(e.Ref, 80), err)
		}
	}
	done, err := mc.Store.SessionFinish(ctx, mc.ProfileID, sid, t.ID, tasks.Outcome{Result: strings.TrimSpace(a.Result), Question: strings.TrimSpace(a.Question)}, actor)
	if err != nil {
		return "", err
	}
	c.msched.mu.Lock()
	delete(c.msched.owned, t.ID)
	c.msched.mu.Unlock()
	return marshal(map[string]any{"task_id": done.ID, "status": done.Status, "next": "Call mission_next for the tasks this unlocks."})
}
