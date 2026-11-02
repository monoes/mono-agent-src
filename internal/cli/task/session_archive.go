package task

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// newSessionArchiveCmd is `task session archive ID [--cascade]`. A session with open sessions
// below it is refused unless the operator passes --cascade, which stops and archives the whole
// open subtree in one write. An agent can do neither.
func (d Deps) newSessionArchiveCmd() *cobra.Command {
	var actor tasks.Actor
	var cascade bool
	cmd := d.operatorSession("archive ID [--cascade]", "Hide a session that is not running (you only); --cascade also stops and hides the open sessions below it", "archive a session", &actor,
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			if !cascade {
				se, err := store.ArchiveSession(cmd.Context(), p.ID, id, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeSession(cmd, p, se, "Archived")
			}
			return d.archiveCascade(cmd.Context(), cmd, store, p, id, actor)
		})
	cmd.Flags().BoolVar(&cascade, "cascade", false, "Also archive every session below it: running ones are stopped (claims released), all in one write; a child's budget does not return to the parent")
	return cmd
}

func (d Deps) archiveCascade(ctx context.Context, cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64, actor tasks.Actor) error {
	ids, err := store.ArchiveSessionCascade(ctx, p.ID, id, actor)
	if err != nil {
		return d.taskErr(err)
	}
	se, err := store.GetSession(ctx, p.ID, id, actor)
	if err != nil {
		return d.taskErr(err)
	}
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "session": se, "archived": ids})
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	fmt.Fprintf(w, "Archived %d sessions, the deepest first: %v\n", len(ids), ids)
	return nil
}
