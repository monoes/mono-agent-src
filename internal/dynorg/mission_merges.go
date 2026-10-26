package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Writer branches a mission has not merged yet are kept in the task store, not in the
// Conductor: when an isolated writer hands its task to review, a "branch" artifact (ref = the
// branch) is attached to the task, and org_merge attaches a "merge" artifact with the same ref
// once the branch is in. mission_finish's merges_remaining is computed from them, so a
// restarted lead lists the same branches. Only the operator's process writes either kind (the
// store refuses an agent), so a worker cannot mark its own branch merged.

func (c *Conductor) recordBranch(ctx context.Context, mc *MissionRun, w *worker) {
	b := w.task
	c.mu.Lock()
	branch, tree := w.branch, w.worktree
	c.mu.Unlock()
	if b == nil || b.verifier || branch == "" {
		return
	}
	art := tasks.ArtifactInput{Kind: tasks.ArtifactBranch, Ref: branch, Note: fmt.Sprintf("worker %s, worktree %s", w.id, tree)}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, b.session, b.task.ID, art, operatorActor()); err != nil {
		c.notice("mission.artifact", fmt.Sprintf("Task #%d: the branch %s of %s could not be recorded: %v", b.task.ID, branch, w.id, err), chatevents.SeverityWarning)
	}
}

// recordMerge notes in the store that org_merge brought a writer's branch in: of the worker
// (w), or, for a Conductor that holds no worker, of the stored branch (stored).
func (c *Conductor) recordMerge(ctx context.Context, w *worker, stored *pendingBranch, from string) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	var session, task int64
	var branch string
	switch {
	case stored != nil:
		sid, err := c.sessionOf(ctx, mc, 0)
		if err != nil {
			return
		}
		session, task, branch = sid, stored.task, stored.branch
	case w != nil && w.task != nil && !w.task.verifier:
		c.mu.Lock()
		branch = w.branch
		c.mu.Unlock()
		session, task = w.task.session, w.task.task.ID
	default:
		return
	}
	// A settled integration task checked the tree before this merge. Requeue it
	// before marking the branch merged, so mission_finish cannot close on a
	// verdict about an older tree. Failure leaves the pending merge intact.
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, session, missionActor())
	if err != nil {
		c.notice("mission.integration", fmt.Sprintf("The merge of %s could not invalidate the integration checks: %v", branch, err), chatevents.SeverityWarning)
		return
	}
	for _, t := range plan.Tasks {
		if !isIntegration(t) || (t.Status != tasks.StatusInProgress && t.Status != tasks.StatusReview && t.Status != tasks.StatusDone) {
			continue
		}
		if _, err := mc.Store.Move(ctx, mc.ProfileID, t.ID, tasks.StatusReady, tasks.Placement{}, operatorActor()); err != nil {
			c.notice("mission.integration", fmt.Sprintf("Task #%d must check the merged tree, but could not be requeued: %v", t.ID, err), chatevents.SeverityWarning)
			return
		}
		_, _ = mc.Store.SessionComment(ctx, mc.ProfileID, session, t.ID, "A writer branch was merged after this integration check started: run and verify the checks again on the merged tree.", missionActor())
	}
	art := tasks.ArtifactInput{Kind: tasks.ArtifactMerge, Ref: branch, Note: "merged by org_merge from " + from}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, session, task, art, operatorActor()); err != nil {
		c.notice("mission.artifact", fmt.Sprintf("Task #%d: the merge of %s could not be recorded: %v", task, branch, err), chatevents.SeverityWarning)
	}
}

// mergesRemaining lists the writer branches of settled tasks that org_merge has not brought
// in, read from the store. Only the branch of the run that handed the task to review (the
// latest) counts: an earlier run's branch was sent back with its failure and is not merged.
func (c *Conductor) mergesRemaining(ctx context.Context, mc *MissionRun, sid int64, all []tasks.Task) []string {
	var out []string
	for _, p := range c.pendingBranches(ctx, mc, sid, all, true) {
		out = append(out, fmt.Sprintf("%s (%s, task #%d)", p.worker, p.branch, p.task))
	}
	return out
}

// branchWorker is the worker id a branch artifact's note names ("worker w3, worktree ...").
func branchWorker(note string) string {
	id, _, _ := strings.Cut(strings.TrimPrefix(note, "worker "), ",")
	return strings.TrimSpace(id)
}
