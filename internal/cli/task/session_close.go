package task

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// newSessionCloseCmd is `session close ID [--note TEXT]`: the operator ends a running or
// paused session as done and gives its claims back. It is the operator's alone: a mission's lead only proposes the
// close (mission_finish), it never makes it. Readiness is the lead's proposal, not a gate:
// the operator may close at any time.
func (d Deps) newSessionCloseCmd() *cobra.Command {
	var actor tasks.Actor
	var note string
	cmd := d.operatorSession("close ID [--note TEXT]", "End a running or paused session as done and release its claims (you only)", "close a session", &actor,
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			se, err := store.CloseSession(cmd.Context(), p.ID, id, tasks.SessionDone, note, actor)
			if err != nil {
				return d.taskErr(err)
			}
			return d.writeSession(cmd, p, se, "Closed")
		})
	cmd.Flags().StringVar(&note, "note", "", "Why it is closed (kept in the session's history)")
	return cmd
}
