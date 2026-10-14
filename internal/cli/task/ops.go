package task

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// printTaskLine says what a command did to a task: the verb, the id, the column the task
// is in and its title, cut as the board cuts it.
func printTaskLine(w io.Writer, verb string, t tasks.Task) {
	fmt.Fprintf(w, "%s #%d (%s): %s\n", verb, t.ID, columnLabel(t.Status), taskCut(t.Title, 70))
}

// writeOneTask prints the result of a command that changed one task: with --json
// {"profile", "task"}, else the profile it acted on and a line for the task. The active
// profile can change under a caller (the app switches it), so the text names the profile.
func (d Deps) writeOneTask(cmd *cobra.Command, p tasks.Profile, t tasks.Task, verb string) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "task": t})
	}
	printProfileLine(cmd.OutOrStdout(), p)
	printTaskLine(cmd.OutOrStdout(), verb, t)
	return nil
}

// writeTasks prints the result of a command that changed several tasks: with --json
// {"profile", "tasks"}, else the profile it acted on and a line for each task.
func (d Deps) writeTasks(cmd *cobra.Command, p tasks.Profile, ts []tasks.Task, verb string) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "tasks": ts})
	}
	printProfileLine(cmd.OutOrStdout(), p)
	for _, t := range ts {
		printTaskLine(cmd.OutOrStdout(), verb, t)
	}
	return nil
}

// Every command of this file is the operator's. Each refuses an agent-driven caller
// first, before it reads its arguments and before it opens the database, and checks the
// number of its arguments itself (not with cobra's Args, which would answer exit 1 with no
// --json document): an argument mistake is exit 3, like every other one of the group.

func (d Deps) newTaskEditCmd() *cobra.Command {
	var title, notes string
	cmd := &cobra.Command{
		Use:   "edit ID [--title T] [--notes TEXT]",
		Short: "Change a task's title or notes (you only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator("edit a task")
			if err != nil {
				return err
			}
			if len(args) != 1 {
				return d.InvalidInput("task edit takes one task id, written 42 or #42 (got %d arguments): the new text goes in --title or --notes", len(args))
			}
			id, err := d.parseTaskID(args[0])
			if err != nil {
				return err
			}
			var e tasks.Edit
			if cmd.Flags().Changed("title") {
				e.Title = &title
			}
			if cmd.Flags().Changed("notes") {
				e.Notes = &notes
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Edit(ctx, p.ID, id, e, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeOneTask(cmd, p, t, "Edited")
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&notes, "notes", "", "New notes (an empty value clears them)")
	return cmd
}

func (d Deps) newTaskMoveCmd() *cobra.Command {
	var before, after string
	var top, bottom bool
	cmd := &cobra.Command{
		Use:   "move ID STATUS [--before ID | --after ID | --top | --bottom]",
		Short: "Move a task to a column, and to a place in it (you only)",
		Long: `STATUS is inbox, ready, in_progress, review or done (archive has its own command).
Without a place the task goes to the column's default: the top of inbox, review
and done, the bottom of ready and in_progress. A task already in that column stays
where it is unless you give a place. The top of Ready is what an AI agent takes
next. Moving a task out of in_progress ends an agent's claim.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator("move a task")
			if err != nil {
				return err
			}
			if len(args) != 2 {
				return d.InvalidInput("task move takes a task id and a column, as in task move 42 ready (got %d arguments)", len(args))
			}
			id, err := d.parseTaskID(args[0])
			if err != nil {
				return err
			}
			to, err := tasks.ParseStatus(args[1])
			if err != nil {
				return d.taskErr(err)
			}
			place := tasks.Placement{Top: top, Bottom: bottom}
			// The store reads a Before or After of 0 as no place at all. A flag that is given
			// must therefore name a task whatever it holds: --before "$ID" with ID unset is
			// refused here, not moved to the column's default end.
			for _, f := range []struct {
				name, value string
				dest        *int64
			}{{"before", before, &place.Before}, {"after", after, &place.After}} {
				if !cmd.Flags().Changed(f.name) {
					continue
				}
				if *f.dest, err = d.parseTaskID(f.value); err != nil {
					return d.InvalidInput("--%s: %v", f.name, err)
				}
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Move(ctx, p.ID, id, to, place, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeOneTask(cmd, p, t, "Moved")
			})
		},
	}
	cmd.Flags().StringVar(&before, "before", "", "Put it just above this task of the column")
	cmd.Flags().StringVar(&after, "after", "", "Put it just below this task of the column")
	cmd.Flags().BoolVar(&top, "top", false, "Put it at the top of the column")
	cmd.Flags().BoolVar(&bottom, "bottom", false, "Put it at the bottom of the column")
	return cmd
}

func (d Deps) newTaskApproveCmd() *cobra.Command {
	var top bool
	cmd := &cobra.Command{
		Use:   "approve ID... [--top]",
		Short: "Move Inbox tasks to Ready, where an AI agent may take them (you only, unless you delegated it)",
		Long: `Approving is the step where you have read a task: agents work only what is in
Ready. The task goes to the bottom of Ready (the queue), or the top with --top.
If any task is not in the Inbox, nothing is approved. An AI agent is refused unless
you ran task agents allow --approve; then a named agent (--as NAME) may approve, and
the history records it as that agent, marked as delegated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			caller := d.callerFor(flagAs(cmd))
			actor, err := caller.operator("approve a task")
			if err != nil {
				// An agent is refused, unless the operator delegated approving (task agents allow
				// --approve), read now. Then it must be named: the history says who approved.
				if !d.agentMay(cmd, caller, func(a tasks.AgentAccess) bool { return a.Approve && a.View }) {
					return err
				}
				if actor, err = caller.agent(); err != nil {
					return err
				}
			}
			if len(args) == 0 {
				return d.InvalidInput("task approve takes the ids of the tasks to approve, written 42 or #42")
			}
			ids, err := d.parseTaskIDs(args)
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ts, err := store.Approve(ctx, p.ID, ids, top, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeTasks(cmd, p, ts, "Approved")
			})
		},
	}
	cmd.Flags().BoolVar(&top, "top", false, "Put the tasks at the top of Ready instead of the bottom")
	return cmd
}

func (d Deps) newTaskArchiveCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "archive ID... | --status STATUS",
		Short: "Hide tasks from the board, keeping them (you only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator("archive tasks")
			if err != nil {
				return err
			}
			// --status that is given and empty is refused with the rest: it must not turn
			// into archiving only the ids that came with it (--status "$COLUMN", COLUMN unset).
			if cmd.Flags().Changed("status") {
				if len(args) > 0 {
					return d.InvalidInput("give task ids or --status, not both")
				}
				st, err := tasks.ParseStatus(status)
				if err != nil {
					return d.taskErr(err)
				}
				return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
					n, err := store.ArchiveStatus(ctx, p.ID, st, actor)
					if err != nil {
						return d.taskErr(err)
					}
					if d.JSONOutput() {
						return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "archived": n})
					}
					noun := "tasks"
					if n == 1 {
						noun = "task"
					}
					printProfileLine(cmd.OutOrStdout(), p)
					fmt.Fprintf(cmd.OutOrStdout(), "Archived %d %s from %s.\n", n, noun, columnLabel(st))
					return nil
				})
			}
			if len(args) == 0 {
				return d.InvalidInput("name the tasks to archive (task archive 42 43) or a whole column (task archive --status done)")
			}
			ids, err := d.parseTaskIDs(args)
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ts, err := store.Archive(ctx, p.ID, ids, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeTasks(cmd, p, ts, "Archived")
			})
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Archive every task of this column (inbox, ready, in_progress, review or done)")
	return cmd
}

func (d Deps) newTaskUnarchiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unarchive ID...",
		Short: "Bring archived tasks back to the column they were archived from (you only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator("unarchive a task")
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return d.InvalidInput("task unarchive takes the ids of the tasks to bring back, written 42 or #42")
			}
			ids, err := d.parseTaskIDs(args)
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ts, err := store.Unarchive(ctx, p.ID, ids, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeTasks(cmd, p, ts, "Restored")
			})
		},
	}
}
