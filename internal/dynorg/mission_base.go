package dynorg

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

var fullObjectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// recordBase notes in the store the repository HEAD the mission folder has when a task first
// starts (a "base" artifact, written under the operator's identity: the store refuses an
// agent): commit evidence for the task must not be part of that history, so a commit that was
// there before the task cannot stand for work done for it. The first start of a task stands;
// a folder that is not a git work tree records nothing, and one with no commit yet records
// tasks.BaseUnborn. A work tree with no recorded start point is refused commit evidence, so the
// record is retried at every start of the task until one is made.
func (c *Conductor) recordBase(ctx context.Context, mc *MissionRun, b *missionBind) {
	if b == nil || mc == nil || c.cfg.Cwd == "" {
		return
	}
	if _, err := gitRun(ctx, c.cfg.Cwd, "rev-parse", "--git-dir"); err != nil {
		return // not a git work tree: nothing to record
	}
	out, err := gitRun(ctx, c.cfg.Cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	head := strings.ToLower(strings.TrimSpace(out))
	if err != nil {
		if !c.unbornHead(ctx) {
			return // a HEAD that cannot be read in a populated repository records nothing: the task stays refused
		}
		head = tasks.BaseUnborn // a work tree with no commit yet: every commit is new work
	} else if !fullObjectID.MatchString(head) {
		return
	}
	art := tasks.ArtifactInput{Kind: tasks.ArtifactBase, Ref: head, Note: "the work folder's HEAD when the task first started"}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, b.session, b.task.ID, art, operatorActor()); err != nil {
		c.notice("mission.artifact", fmt.Sprintf("Task #%d: its start point could not be recorded: %v", b.task.ID, err), chatevents.SeverityWarning)
	}
}

// unbornHead is true only for a repository that has no commit at all: HEAD names a branch that
// does not exist and no ref exists anywhere. A corrupt HEAD of a populated repository is not.
func (c *Conductor) unbornHead(ctx context.Context) bool {
	ref, err := gitRun(ctx, c.cfg.Cwd, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(ref) == "" {
		return false
	}
	if _, err := gitRun(ctx, c.cfg.Cwd, "rev-parse", "--verify", "--quiet", strings.TrimSpace(ref)+"^{commit}"); err == nil {
		return false
	}
	refs, err := gitRun(ctx, c.cfg.Cwd, "for-each-ref", "--count=1")
	return err == nil && strings.TrimSpace(refs) == ""
}
