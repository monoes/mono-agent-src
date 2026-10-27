package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// closeRefusal is why a mission is not ready to close.
type closeRefusal struct{ why string }

func (e *closeRefusal) Error() string { return "the session is not closed: " + e.why }

// verdictMarker starts the first line of a final report artifact's note: the id of the
// integration task's verdict event that the report is about.
const verdictMarker = "integration verdict event #"

// readyToClose is the predicate for proposing a mission's close; the conductor never closes
// a session, the operator does. It reads the session and its plan afresh from the store and
// refuses unless the session is running, every non-epic task is done or verified, no task is
// blocked, no writer branch is waiting for its merge, and the integration check task exists,
// is done or verified, and is newer than every other task. It returns the integration task.
func (c *Conductor) readyToClose(ctx context.Context, mc *MissionRun, sid int64) (tasks.Task, error) {
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return tasks.Task{}, err
	}
	if se.State != tasks.SessionRunning || se.Archived {
		return tasks.Task{}, &closeRefusal{fmt.Sprintf("session #%d is %s, not running", sid, se.State)}
	}
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return tasks.Task{}, err
	}
	var integration *tasks.Task
	for i, t := range plan.Tasks {
		if t.Blocked {
			return tasks.Task{}, &closeRefusal{fmt.Sprintf("task #%d is blocked", t.ID)}
		}
		if integration == nil && isIntegration(t) {
			integration = &plan.Tasks[i]
		}
	}
	for _, t := range plan.Tasks {
		if t.Kind != tasks.KindEpic && !settled(t) {
			return tasks.Task{}, &closeRefusal{fmt.Sprintf("task #%d is %s, not done or verified", t.ID, t.Status)}
		}
	}
	if integration == nil {
		return tasks.Task{}, &closeRefusal{"the plan has no integration check task"}
	}
	for _, t := range plan.Tasks {
		if t.Kind == tasks.KindEpic {
			continue
		}
		if who, bad := c.verdictUntrusted(ctx, mc, sid, t); bad {
			return tasks.Task{}, &closeRefusal{fmt.Sprintf("task #%d was judged by %q, not by its verifier (%s)", t.ID, who, verdictByUnexpectedActor)}
		}
	}
	if m := c.mergesRemaining(ctx, mc, sid, plan.Tasks); len(m) > 0 {
		return tasks.Task{}, &closeRefusal{fmt.Sprintf("%d writer branch(es) are not merged yet", len(m))}
	}
	if why := c.integrationStale(ctx, mc, sid, plan, *integration); why != "" {
		return tasks.Task{}, &closeRefusal{why}
	}
	return *integration, nil
}

// proposeClose records that the mission is ready for the operator to close and leaves the
// session running. It refuses unless readyToClose holds. The proposal is keyed on the
// integration task's current verdict: the final report (written by the operator's process,
// the verdict's event id on its first line) is attached once per verdict, and emit (the
// card) runs once per verdict too, recorded by a close_proposed session event written
// after it. So a crash between the two steps is repaired by the next call, and work
// reopened after a proposal gets a second report. proposed is false when this verdict was
// already proposed: nothing is written then. Calls are serialised.
func (c *Conductor) proposeClose(ctx context.Context, mc *MissionRun, sid int64, report string, emit func()) (proposed bool, err error) {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	integration, err := c.readyToClose(ctx, mc, sid)
	if err != nil {
		return false, err
	}
	d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, integration.ID, missionActor())
	if err != nil {
		return false, fmt.Errorf("the integration check #%d could not be read: %w", integration.ID, err)
	}
	mark := fmt.Sprintf("%s%d", verdictMarker, integrationVerdictID(d))
	attached := false
	for _, a := range d.Artifacts {
		if a.Kind == "report" && a.Ref == tasks.FinalReportRef && a.Actor == operatorActor().Label() && strings.HasPrefix(a.Note, mark+"\n") {
			attached = true
		}
	}
	if !attached {
		art := tasks.ArtifactInput{Kind: "report", Ref: tasks.FinalReportRef, Note: mark + "\n\n" + report}
		if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, sid, integration.ID, art, operatorActor()); err != nil {
			return false, fmt.Errorf("the final report could not be attached to task #%d: %w", integration.ID, err)
		}
	} else {
		evs, err := mc.Store.SessionEvents(ctx, mc.ProfileID, sid, 0, missionActor())
		if err != nil {
			return false, err
		}
		for _, e := range evs {
			if e.Kind == tasks.EventCloseProposed && e.Note == mark {
				return false, nil
			}
		}
	}
	if emit != nil {
		emit()
	}
	if err := mc.Store.AddSessionEvent(ctx, mc.ProfileID, sid, tasks.EventCloseProposed, mark, operatorActor()); err != nil {
		return true, fmt.Errorf("the proposal could not be recorded: %w", err)
	}
	return true, nil
}
