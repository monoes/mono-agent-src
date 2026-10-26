package dynorg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

// cleanupWorktrees removes the turn's worktrees at its end, keeping the
// branches that hold unmerged work, then lets go of the turn's lock.
func (c *Conductor) cleanupWorktrees() {
	if c.iso == nil {
		return
	}
	c.removeVerifierTrees()
	ctx := context.WithoutCancel(c.ctx)
	type tree struct{ id, path, branch string }
	var trees []tree
	c.mu.Lock()
	for _, id := range c.order {
		if w := c.workers[id]; w.worktree != "" {
			trees = append(trees, tree{w.id, w.worktree, w.branch})
		}
	}
	c.mu.Unlock()
	for _, w := range trees {
		r := removeTree(ctx, c.iso.cwd, c.iso.base, w.path, w.branch)
		c.reportCleanup(w.id, r)
	}
	c.iso.unlock()
	releaseTurnDir(c.iso.base, c.iso.turnDir)
	_, _ = gitRun(ctx, c.iso.cwd, "worktree", "prune")
}

// reportCleanup journals what removing a worker's worktree left behind.
func (c *Conductor) reportCleanup(id string, r TreeCleanup) {
	if len(r.Ignored) > 0 {
		c.cfg.Emit.Emit(chatevents.EventNotice, chatevents.NoticePayload{
			Code: NoticeIgnoredRemoved, Severity: chatevents.SeverityInfo,
			Message: fmt.Sprintf("%s's worktree held files git ignores, removed with it: %s", id, strings.Join(r.Ignored, ", ")),
		})
	}
	switch {
	case r.Error != "":
		c.cfg.Emit.Emit(chatevents.EventNotice, chatevents.NoticePayload{
			Code: NoticeBranchKept, Severity: chatevents.SeverityWarning,
			Message: fmt.Sprintf("%s's worktree %s was left in place: %s", id, r.Path, r.Error),
		})
	case r.BranchKept:
		c.cfg.Emit.Emit(chatevents.EventNotice, chatevents.NoticePayload{
			Code: NoticeBranchKept, Severity: chatevents.SeverityInfo,
			Message: fmt.Sprintf("%s's work was not merged into a branch; it is kept on branch %s (git merge %s to take it, git branch -D %s to drop it).", id, r.Branch, r.Branch, r.Branch),
		})
	}
}

// TreeCleanup is what cleaning one worker's worktree did.
type TreeCleanup struct {
	Turn       string `json:"turn,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	Removed    bool   `json:"removed"`
	BranchKept bool   `json:"branch_kept"`
	// Ignored lists files git ignores that were removed with the worktree
	// (at most maxIgnoredListed, then "…").
	Ignored []string `json:"ignored_removed,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// statusPorcelain is `git status --porcelain` in dir; a var so a test can
// make it fail.
var statusPorcelain = func(ctx context.Context, dir string) (string, error) {
	return gitRun(ctx, dir, "status", "--porcelain")
}

// removeTree removes one worktree (after committing anything left in it)
// and its branch once another branch contains it. A worktree whose changes
// can't be committed, or whose state can't be read, is kept. Nothing
// outside base is touched.
func removeTree(ctx context.Context, repo, base, path, branch string) TreeCleanup {
	r := TreeCleanup{Path: path, Branch: branch}
	if !within(base, path) {
		r.Error = "refused: the path is outside " + base
		return r
	}
	if _, err := os.Lstat(path); err == nil {
		if !isWorktreeRoot(ctx, path) {
			// Not (or no longer) a worktree: git would work on the chat
			// folder's repository from here. Only an empty leftover goes.
			if os.Remove(path) != nil {
				r.Error = "it is not a git worktree and not empty"
				return r
			}
			r.Removed = true
			return r
		}
		if err := commitAll(ctx, path, branch, "mono-agent: work left in the worktree"); err != nil {
			// Changes that can't be committed stay where they are, and so
			// does everything when their state can't even be read.
			dirty, serr := statusPorcelain(ctx, path)
			if serr != nil {
				r.Error = "its state could not be read: " + serr.Error()
				return r
			}
			if strings.TrimSpace(dirty) != "" {
				r.Error = "it has changes that could not be committed: " + err.Error() +
					". Commit them on a branch there to keep them, or drop them with `git worktree remove --force " + path + "`"
				return r
			}
		}
		r.Ignored = ignoredFiles(ctx, path)
		if _, err := gitRun(ctx, repo, "worktree", "remove", "--force", path); err != nil {
			if !within(base, path) {
				r.Error = "refused: the path is outside " + base
				return r
			}
			if err := os.RemoveAll(path); err != nil {
				r.Error = err.Error()
				return r
			}
		}
	}
	_, _ = gitRun(ctx, repo, "worktree", "prune")
	r.Removed = true
	if _, err := gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return r // no branch (never created, or already gone)
	}
	if !containedElsewhere(ctx, repo, branch) {
		r.BranchKept = true
		return r
	}
	if _, err := gitRun(ctx, repo, "branch", "-D", branch); err != nil {
		r.BranchKept = true
	}
	return r
}

// containedElsewhere reports whether another local branch holds every
// commit of branch, so deleting it loses nothing. A detached HEAD doesn't
// count: its commits are lost once it moves.
func containedElsewhere(ctx context.Context, repo, branch string) bool {
	out, err := gitRun(ctx, repo, "for-each-ref", "--format=%(refname)", "--contains", "refs/heads/"+branch, "refs/heads/")
	if err != nil {
		return false
	}
	for _, ref := range strings.Fields(out) {
		if ref != "refs/heads/"+branch {
			return true
		}
	}
	return false
}

// ignoredFiles lists what git ignores in a worktree (folders collapsed),
// capped for a notice.
func ignoredFiles(ctx context.Context, dir string) []string {
	out, err := gitRun(ctx, dir, "status", "--porcelain", "--ignored")
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "!! "); ok {
			if len(files) == maxIgnoredListed {
				files = append(files, "…")
				break
			}
			files = append(files, p)
		}
	}
	return files
}

// ReconcileWorktrees removes the worktrees under cwd that belong to turns
// no longer running, keeping their unmerged branches. A turn counts as
// running when active says so (not finalized) or when its process still
// holds its lock. The app's startup reconcile and the start of each
// isolated turn call it.
func ReconcileWorktrees(ctx context.Context, cwd string, active func(turnID string) bool) []TreeCleanup {
	base, err := safeBase(cwd)
	if err != nil {
		return nil
	}
	turns, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	if _, err := gitRun(ctx, cwd, "rev-parse", "--git-dir"); err != nil {
		return nil
	}
	var out []TreeCleanup
	for _, t := range turns {
		// Symlinks are not followed, and names that can't be ours are left.
		if !t.IsDir() || !treeID.MatchString(t.Name()) || active(t.Name()) {
			continue
		}
		turnDir := filepath.Join(base, t.Name())
		unlock, err := daemonhb.LockFile(filepath.Join(turnDir, turnLockName))
		if errors.Is(err, daemonhb.ErrHeld) {
			continue // its turn is still running in some process
		}
		if err != nil {
			out = append(out, TreeCleanup{Turn: t.Name(), Path: turnDir, Error: "skipped: its lock could not be taken: " + err.Error()})
			continue
		}
		trees, _ := os.ReadDir(turnDir)
		for _, w := range trees {
			if !w.IsDir() || !treeID.MatchString(w.Name()) {
				continue
			}
			r := removeTree(ctx, cwd, base, filepath.Join(turnDir, w.Name()), branchName(t.Name(), w.Name()))
			r.Turn, r.Agent = t.Name(), w.Name()
			out = append(out, r)
		}
		unlock()
		releaseTurnDir(base, turnDir)
	}
	_, _ = gitRun(ctx, cwd, "worktree", "prune")
	return out
}
