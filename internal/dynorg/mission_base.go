package dynorg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// tasks.BaseUnborn, one git cannot read records tasks.BaseUnreadable. A work tree with no recorded start point is refused commit evidence, so the
// record is retried at every start of the task until one is made.
func (c *Conductor) recordBase(ctx context.Context, mc *MissionRun, b *missionBind) {
	if b == nil || mc == nil || c.cfg.Cwd == "" {
		return
	}
	if _, err := gitRun(ctx, c.cfg.Cwd, "rev-parse", "--git-dir"); err != nil {
		if ctx.Err() != nil {
			return // retried at the next start
		}
		if fi, serr := os.Stat(c.cfg.Cwd); serr != nil || !fi.IsDir() {
			return // no folder: nothing to record, and the check refuses a missing folder itself
		}
		_, lerr := os.Lstat(filepath.Join(c.cfg.Cwd, ".git"))
		if strings.HasPrefix(strings.ToLower(err.Error()), "fatal: not a git repository") && errors.Is(lerr, os.ErrNotExist) {
			return // not a git work tree: nothing to record
		}
		// a folder git cannot read (a broken configuration, say) must not look like no repository later
		c.addBase(ctx, mc, b, tasks.BaseUnreadable, operatorActor())
		return
	}
	out, err := gitRun(ctx, c.cfg.Cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	head := strings.ToLower(strings.TrimSpace(out))
	if err != nil {
		if ctx.Err() != nil {
			return // retried at the next start
		}
		if c.unbornHead(ctx) {
			head = tasks.BaseUnborn // a work tree with no commit yet: every commit is new work
		} else {
			// an initial HEAD that cannot be read and is not proven unborn is an unknown start
			// state: it must not turn into "no repository" if the metadata disappears later
			head = tasks.BaseUnreadable
		}
	} else if !fullObjectID.MatchString(head) {
		head = tasks.BaseUnreadable
	}
	c.addBase(ctx, mc, b, head, operatorActor())
}

func (c *Conductor) addBase(ctx context.Context, mc *MissionRun, b *missionBind, head string, actor tasks.Actor) {
	art := tasks.ArtifactInput{Kind: tasks.ArtifactBase, Ref: head, Note: "the work folder's HEAD when the task first started"}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, b.session, b.task.ID, art, actor); err != nil {
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
