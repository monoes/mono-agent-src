package dynorg

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// closeRefusal is why operatorClose will not close a session.
type closeRefusal struct{ why string }

func (e *closeRefusal) Error() string { return "the session is not closed: " + e.why }

// operatorClose closes a mission's session as done, as the operator, and is the one place
// the conductor does that. It reads the session and its plan afresh from the store and
// refuses unless the session is running, every non-epic task is done or verified, no task is
// blocked, no writer branch is waiting for its merge, and the integration check task exists
// and is done or verified. Only then is the final report attached to the integration task
// and the session closed.
func (c *Conductor) operatorClose(ctx context.Context, mc *MissionRun, sid int64, report, note string) (tasks.Session, error) {
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return tasks.Session{}, err
	}
	if se.State != tasks.SessionRunning || se.Archived {
		return tasks.Session{}, &closeRefusal{fmt.Sprintf("session #%d is %s, not running", sid, se.State)}
	}
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return tasks.Session{}, err
	}
	var integration *tasks.Task
	for i, t := range plan.Tasks {
		if t.Blocked {
			return tasks.Session{}, &closeRefusal{fmt.Sprintf("task #%d is blocked", t.ID)}
		}
		if integration == nil && isIntegration(t) {
			integration = &plan.Tasks[i]
		}
	}
	for _, t := range plan.Tasks {
		if t.Kind != tasks.KindEpic && !settled(t) {
			return tasks.Session{}, &closeRefusal{fmt.Sprintf("task #%d is %s, not done or verified", t.ID, t.Status)}
		}
	}
	if integration == nil {
		return tasks.Session{}, &closeRefusal{"the plan has no integration check task"}
	}
	for _, t := range plan.Tasks {
		if t.Kind == tasks.KindEpic {
			continue
		}
		if who, bad := c.verdictUntrusted(ctx, mc, sid, t); bad {
			return tasks.Session{}, &closeRefusal{fmt.Sprintf("task #%d was judged by %q, not by its verifier (%s)", t.ID, who, verdictByUnexpectedActor)}
		}
	}
	if m := c.mergesRemaining(ctx, mc, sid, plan.Tasks); len(m) > 0 {
		return tasks.Session{}, &closeRefusal{fmt.Sprintf("%d writer branch(es) are not merged yet", len(m))}
	}
	if why := c.integrationStale(ctx, mc, sid, plan, *integration); why != "" {
		return tasks.Session{}, &closeRefusal{why}
	}
	art := tasks.ArtifactInput{Kind: "report", Ref: "final-report", Note: report}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, sid, integration.ID, art, operatorActor()); err != nil {
		return tasks.Session{}, fmt.Errorf("the final report could not be attached to task #%d: %w", integration.ID, err)
	}
	return mc.Store.CloseSession(ctx, mc.ProfileID, sid, tasks.SessionDone, note, operatorActor())
}
