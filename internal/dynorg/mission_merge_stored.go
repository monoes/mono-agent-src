package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// pendingBranch is a writer branch the store holds a "branch" artifact for and no later
// "merge" artifact.
type pendingBranch struct {
	task   int64
	branch string
	worker string // the worker id the artifact's note names
}

// pendingBranches lists the unmerged writer branches of a session, read from the store (so
// any Conductor sees the same list). Only the newest branch of a task counts. With
// settledOnly it skips tasks that are not done or verified.
func (c *Conductor) pendingBranches(ctx context.Context, mc *MissionRun, sid int64, all []tasks.Task, settledOnly bool) []pendingBranch {
	var out []pendingBranch
	for _, t := range all {
		if t.Kind == tasks.KindEpic || (settledOnly && !settled(t)) {
			continue
		}
		d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, t.ID, missionActor())
		if err != nil {
			continue
		}
		var branch *tasks.Artifact
		for i := len(d.Artifacts) - 1; i >= 0 && branch == nil; i-- {
			if d.Artifacts[i].Kind == tasks.ArtifactBranch {
				branch = &d.Artifacts[i]
			}
		}
		if branch == nil {
			continue
		}
		merged := false
		for _, a := range d.Artifacts {
			if a.Kind == tasks.ArtifactMerge && a.Ref == branch.Ref && a.ID > branch.ID {
				merged = true
			}
		}
		if !merged {
			out = append(out, pendingBranch{task: t.ID, branch: branch.Ref, worker: branchWorker(branch.Note)})
		}
	}
	return out
}

// storedBranch finds the unmerged branch org_merge's agent_id names, for a Conductor that
// holds no such worker: the id is the writer's worker id or its branch. Several matches
// (worker ids repeat from turn to turn) are refused with the branches listed.
func (c *Conductor) storedBranch(ctx context.Context, id string) (*pendingBranch, error) {
	mc, err := c.missions()
	if err != nil {
		return nil, fmt.Errorf("no worker %q", id)
	}
	sid, err := c.sessionOf(ctx, mc, 0)
	if err != nil {
		return nil, fmt.Errorf("no worker %q", id)
	}
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return nil, fmt.Errorf("no worker %q: %v", id, err)
	}
	var match []pendingBranch
	for _, p := range c.pendingBranches(ctx, mc, sid, plan.Tasks, false) {
		if p.worker == id || p.branch == id {
			match = append(match, p)
		}
	}
	switch len(match) {
	case 0:
		return nil, fmt.Errorf("no worker %q, and the mission has no unmerged branch of it", id)
	case 1:
		return &match[0], nil
	}
	var names []string
	for _, p := range match {
		names = append(names, p.branch)
	}
	return nil, fmt.Errorf("%q matches several unmerged branches (%s): give the branch as agent_id", id, strings.Join(names, ", "))
}

// hasTaskWorkerLocked is taskWorker for a caller that holds c.mu.
func (c *Conductor) hasTaskWorkerLocked(taskID int64) bool {
	for _, id := range c.order {
		if w := c.workers[id]; w != nil && w.task != nil && w.task.task.ID == taskID && running(w.status) {
			return true
		}
	}
	return false
}
