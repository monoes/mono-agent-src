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
// a folder that is not a git work tree records nothing.
func (c *Conductor) recordBase(ctx context.Context, mc *MissionRun, b *missionBind) {
	if b == nil || mc == nil || c.cfg.Cwd == "" {
		return
	}
	out, err := gitRun(ctx, c.cfg.Cwd, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	head := strings.ToLower(strings.TrimSpace(out))
	if err != nil || !fullObjectID.MatchString(head) {
		return
	}
	art := tasks.ArtifactInput{Kind: tasks.ArtifactBase, Ref: head, Note: "the work folder's HEAD when the task first started"}
	if _, err := mc.Store.AddArtifact(ctx, mc.ProfileID, b.session, b.task.ID, art, operatorActor()); err != nil {
		c.notice("mission.artifact", fmt.Sprintf("Task #%d: its start point could not be recorded: %v", b.task.ID, err), chatevents.SeverityWarning)
	}
}
