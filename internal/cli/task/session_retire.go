package task

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// newSessionRetireWorkerCmd is `task session retire-worker`: the operator ends the idle worker of a
// mission task, which the lead's org_clear_worker and org_compact_worker refuse to do because the
// worker's context is part of the task's evidence.
func (d Deps) newSessionRetireWorkerCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "retire-worker SESSION TASK [--note TEXT]",
		Short: "Retire the idle worker of a session task: it takes no more follow-ups (you only)",
		Long: `Retire the worker a mission's conductor ran on a task, once it has finished: the lead can no longer
message it, and its stored session is dropped so a later turn does not load it. The task keeps everything
the worker left, byte for byte: its comments, artifacts, commit references and result; the worker's own
report is kept too. The retirement is recorded in the session's history with your note.

It refuses a worker that is still on the task (the task is claimed, or a mission token of it is live) and
a task no worker took. Stored workers with no link to a task (saved before workers were linked, or not
mission workers) cannot be matched: the result counts them and the text says so. Saying it twice changes nothing. Only you may; an agent is refused.

This is for the worker of a task run by the mission's conductor. A task running as a child coder session
(off by default) is stopped with coder sessions stop and is not touched by this verb.`,
		Example: "  monoagentcli task session retire-worker 3 12 --note \"its context is stale\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "retire a session's worker")
			if err != nil {
				return err
			}
			if len(args) != 2 {
				return d.InvalidInput("task session retire-worker takes a session id and a task id, as in retire-worker 3 12 (got %d arguments)", len(args))
			}
			session, err := d.parseSessionID(args[0])
			if err != nil {
				return err
			}
			task, err := d.parseTaskID(args[1])
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				r, err := store.RetireWorker(ctx, p.ID, session, task, note, actor)
				if err != nil {
					return d.taskErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "retired": r})
				}
				w := cmd.OutOrStdout()
				printProfileLine(w, p)
				if r.AlreadyRetired {
					fmt.Fprintf(w, "The worker of task #%d (session #%d) was already retired.\n", r.Task, r.Session)
					if r.UnmatchedStoredWorkers > 0 {
						fmt.Fprintf(w, "Note: %d stored worker(s) of this session's conversation have no link to a task and are not retired.\n", r.UnmatchedStoredWorkers)
					}
					return nil
				}
				fmt.Fprintf(w, "Retired the worker of task #%d (session #%d). Its report and the task's evidence are kept.\n", r.Task, r.Session)
				if r.UnmatchedStoredWorkers > 0 {
					fmt.Fprintf(w, "Note: %d stored worker(s) of this session's conversation have no link to a task (saved before workers were linked, or not mission workers), so their purpose cannot be determined. They are not retired and a later turn may load them.\n", r.UnmatchedStoredWorkers)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "Why it is retired (kept in the session's history)")
	return cmd
}
