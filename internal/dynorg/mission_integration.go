package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The integration check judges the whole mission, so it must have run on the tree as it is
// now. Three rules keep its verdict from going stale:
//
//   - a plan's integration check depends on every task that is not a check (ValidatePlan);
//   - a task added to a running mission gets an edge from the integration check, which is
//     requeued when it had begun (waitForNewTasks);
//   - the mission closes only when the check's verdict is newer than the last hand-back,
//     verdict or merge of every other task (integrationStale).

// requeueIntegration sends an integration check that had begun back to Ready: only the
// operator's process may move a task out of Review or Done.
func requeueIntegration(ctx context.Context, store *tasks.Store, profileID string, t tasks.Task) error {
	if t.Status != tasks.StatusInProgress && t.Status != tasks.StatusReview && t.Status != tasks.StatusDone {
		return nil
	}
	_, err := store.Move(ctx, profileID, t.ID, tasks.StatusReady, tasks.Placement{}, operatorActor())
	return err
}

// settleEventID is the newest event that settled the task's work: the integration's own
// verdict is its latest pass or close.
func integrationVerdictID(d tasks.SessionTaskDetail) int64 {
	var id int64
	for _, e := range d.Events {
		if e.Kind == "verified" || (e.Kind == "moved" && e.ToStatus == string(tasks.StatusDone)) {
			id = max(id, e.ID)
		}
	}
	return id
}

// lastChangeID is the newest event that changed the tree a task stands for: a hand-back, a
// verdict, a merge of its branch.
func lastChangeID(d tasks.SessionTaskDetail) int64 {
	var id int64
	for _, e := range d.Events {
		switch {
		case e.Kind == "result" || e.Kind == "question" || e.Kind == "verified":
			id = max(id, e.ID)
		case e.Kind == "artifact" && strings.HasPrefix(e.Note, tasks.ArtifactMerge+" "):
			id = max(id, e.ID)
		}
	}
	return id
}

// integrationStale says why the integration check's verdict does not cover the tree ("" =
// it does): some other task changed after it.
func (c *Conductor) integrationStale(ctx context.Context, mc *MissionRun, sid int64, plan tasks.SessionPlan, integration tasks.Task) string {
	di, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, integration.ID, missionActor())
	if err != nil {
		return "the integration check could not be read: " + err.Error()
	}
	verdict := integrationVerdictID(di)
	for _, t := range plan.Tasks {
		if t.Kind == tasks.KindEpic || t.ID == integration.ID {
			continue
		}
		d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, t.ID, missionActor())
		if err != nil {
			continue
		}
		if lastChangeID(d) > verdict {
			return fmt.Sprintf("task #%d changed after the integration check ran (its verdict is about an older tree)", t.ID)
		}
	}
	return ""
}

// waitForNewTasks makes the session's integration check wait for tasks added to a running
// mission: an edge from it to each new task that is not a check, and a requeue when it had
// begun.
func (m *MissionConfig) waitForNewTasks(ctx context.Context, se tasks.Session, ids []int64) error {
	if se.State == tasks.SessionDraft || len(ids) == 0 {
		return nil
	}
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, se.ID, m.actor(se.ID))
	if err != nil {
		return err
	}
	var integration *tasks.Task
	added := map[int64]bool{}
	for _, id := range ids {
		added[id] = true
	}
	var on []int64
	for i, t := range plan.Tasks {
		switch {
		case integration == nil && isIntegration(t) && !added[t.ID]:
			integration = &plan.Tasks[i]
		case added[t.ID] && t.Kind == tasks.KindTask:
			on = append(on, t.ID)
		}
	}
	if integration == nil || len(on) == 0 {
		return nil
	}
	if err := requeueIntegration(ctx, m.Store, m.ProfileID, *integration); err != nil {
		return fmt.Errorf("the integration check #%d must wait for the new tasks but could not be requeued: %w", integration.ID, err)
	}
	if _, err := m.Store.AddDependencies(ctx, m.ProfileID, se.ID, integration.ID, on, m.actor(se.ID)); err != nil {
		return fmt.Errorf("the integration check #%d could not be made to wait for the new tasks: %w", integration.ID, err)
	}
	return nil
}

// integrationRejections is the plan rule: an integration check in the batch depends on every
// task of the batch and of the session that is not a check or an epic.
func integrationRejections(batch []PlanTask, existing []tasks.Task) []Rejection {
	var out []Rejection
	for _, t := range batch {
		if !(t.Kind == kindCheck && strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Title)), "integration")) {
			continue
		}
		deps := map[string]bool{}
		for _, d := range t.DependsOn {
			deps[d] = true
		}
		var missing []string
		for _, o := range batch {
			if (o.Kind == "" || o.Kind == kindTask) && !deps[o.Ref] {
				missing = append(missing, o.Ref)
			}
		}
		for _, e := range existing {
			if e.Kind == tasks.KindTask && !deps[fmt.Sprintf("#%d", e.ID)] {
				missing = append(missing, fmt.Sprintf("#%d", e.ID))
			}
		}
		if len(missing) > 0 {
			out = append(out, Rejection{Ref: t.Ref, Title: t.Title, Reasons: []string{
				"the integration check must depend on every task that is not a check, so it runs on the finished tree: add " + strings.Join(missing, ", ") + " to depends_on"}})
		}
	}
	return out
}
