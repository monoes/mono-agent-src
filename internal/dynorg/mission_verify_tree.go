package dynorg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A verifier runs read only in the chat folder, which lacks a writer's unmerged work. With
// isolated writers the task's "branch" artifact in the store names the branch the executor
// worked on, so the verifier gets a detached checkout of that branch as its working
// directory: a fresh one (never the executor's own tree, which a verifier's build output
// must not dirty), removed when its run ends.

// branchRef is what a stored branch must look like before it is handed to git:
// monoagent/<turn>/<worker>, both parts a tree id. The artifact is data from the store.
var branchRef = regexp.MustCompile(`^monoagent/[A-Za-z0-9_][A-Za-z0-9_-]{0,127}/[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)

// validBranch says whether ref may be used as a writer branch.
func validBranch(ref string) bool { return branchRef.MatchString(ref) }

// unmergedBranch is the newest branch artifact of a task that no later merge artifact
// settled, or nil.
func unmergedBranch(d tasks.SessionTaskDetail) *tasks.Artifact {
	var branch *tasks.Artifact
	for i := len(d.Artifacts) - 1; i >= 0 && branch == nil; i-- {
		if d.Artifacts[i].Kind == tasks.ArtifactBranch {
			branch = &d.Artifacts[i]
		}
	}
	if branch == nil {
		return nil
	}
	for _, a := range d.Artifacts {
		if a.Kind == tasks.ArtifactMerge && a.Ref == branch.Ref && a.ID > branch.ID {
			return nil
		}
	}
	return branch
}

// verifyBranchOf is the branch the verifier should look at: the stored one, else the one the
// executor's worker still held in this Conductor.
func verifyBranchOf(d tasks.SessionTaskDetail, inMemory string) string {
	if a := unmergedBranch(d); a != nil {
		return a.Ref
	}
	return inMemory
}

// addVerifierTree gives a verifier of a task with an unmerged branch a detached checkout of
// it as its working directory. It does nothing when writers are not isolated (the work is in
// the chat folder) or the branch is not there; the verifier then runs in the chat folder.
func (c *Conductor) addVerifierTree(w *worker) {
	branch := w.task.verifyBranch
	if c.iso == nil || branch == "" || !treeID.MatchString(w.id) {
		return
	}
	warn := func(why string) {
		c.cfg.Emit.Emit(chatevents.EventNotice, chatevents.NoticePayload{
			Code: NoticeWritersShared, Severity: chatevents.SeverityWarning,
			Message: fmt.Sprintf("%s checks task #%d in the chat folder: %s.", w.id, w.task.task.ID, why),
		})
	}
	if !validBranch(branch) {
		warn(fmt.Sprintf("the branch %q is not one of ours", boundText(branch, 80)))
		return
	}
	if _, err := gitRun(c.ctx, c.iso.cwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		warn(fmt.Sprintf("the branch %s no longer exists", branch))
		return
	}
	path := filepath.Join(c.iso.turnDir, w.id)
	if _, err := gitRun(c.ctx, c.iso.cwd, "worktree", "add", "--quiet", "--detach", path, "refs/heads/"+branch); err != nil {
		warn(fmt.Sprintf("a checkout of %s could not be made (%v)", branch, err))
		return
	}
	c.mu.Lock()
	w.verifyTree = path
	w.workDir = filepath.Join(path, filepath.FromSlash(c.iso.prefix))
	c.mu.Unlock()
}

// removeVerifierTree removes the verifier's checkout; it is idempotent.
func (c *Conductor) removeVerifierTree(w *worker) {
	c.mu.Lock()
	path := w.verifyTree
	w.verifyTree = ""
	c.mu.Unlock()
	if path == "" || c.iso == nil || !within(c.iso.base, path) {
		return
	}
	ctx := context.WithoutCancel(c.ctx)
	if _, err := gitRun(ctx, c.iso.cwd, "worktree", "remove", "--force", path); err != nil {
		if os.RemoveAll(path) != nil {
			return
		}
	}
	_, _ = gitRun(ctx, c.iso.cwd, "worktree", "prune")
}

// removeVerifierTrees removes every verifier checkout still there (the turn's end).
func (c *Conductor) removeVerifierTrees() {
	c.mu.Lock()
	var ws []*worker
	for _, id := range c.order {
		if w := c.workers[id]; w != nil && w.verifyTree != "" {
			ws = append(ws, w)
		}
	}
	c.mu.Unlock()
	for _, w := range ws {
		c.removeVerifierTree(w)
	}
}

// verifierBranchNote is the line of the verifier's brief that says where the work is.
func verifierBranchNote(branch string, checkedOut bool) string {
	if branch == "" {
		return ""
	}
	if checkedOut {
		return fmt.Sprintf("- the executor worked on the branch %s, which is not merged yet: your working directory is a read-only checkout of it, so the executor's code is here (look at `git log` and `git diff` against the base)\n", boundText(branch, 200))
	}
	return fmt.Sprintf("- the executor worked on the branch %s (git can show it: `git diff` and `git log` against it)\n", boundText(branch, 200))
}
