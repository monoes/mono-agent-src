package task

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// agentMay says whether the caller is an agent the operator has given the right that pick selects,
// on the profile of cfg. It reads the database now, caches nothing, and fails closed: when the
// database or the profile cannot be opened or read the answer is no, and the command gives the
// refusal an agent has always got. It is what lets task approve and task board serve an agent.
func (d Deps) agentMay(cmd *cobra.Command, caller Caller, pick func(tasks.AgentAccess) bool) bool {
	if !caller.isAgent() {
		return false
	}
	db, err := d.OpenDB()
	if err != nil {
		return false
	}
	defer db.Close()
	store := tasks.NewStore(db.DB)
	p, err := store.Profile(cmd.Context(), d.ProfileID())
	if err != nil {
		return false
	}
	a, err := store.AgentAccess(cmd.Context(), p.ID)
	return err == nil && pick(a)
}

// printAgentAccess says what agents may do on the profile, in text.
func printAgentAccess(w io.Writer, p tasks.Profile, a tasks.AgentAccess) {
	yes := func(b bool) string {
		if b {
			return "allowed"
		}
		return "not allowed (the default)"
	}
	printProfileLine(w, p)
	fmt.Fprintf(w, "AI agents seeing the board and the Inbox: %s\n", yes(a.View))
	fmt.Fprintf(w, "AI agents approving Inbox tasks:           %s\n", yes(a.Approve))
}

func (d Deps) writeAgentAccess(cmd *cobra.Command, p tasks.Profile, a tasks.AgentAccess) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "view": a.View, "approve": a.Approve})
	}
	printAgentAccess(cmd.OutOrStdout(), p, a)
	return nil
}

func (d Deps) newTaskAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "What AI agents may do with the Inbox of this profile: show, allow, deny (allow and deny: you only)",
		Long: `By default an AI agent works only the tasks you moved to Ready: it does not see the
board or the Inbox unless it names the Inbox, and it never approves. You can delegate
two abilities, for one profile at a time, and take them back at any moment:

  --view      agents see the whole board (task board) and the Inbox in task list
              and in the task_list tool, without naming it
  --approve   a NAMED agent (--as NAME) moves Inbox tasks to Ready, in the history
              as that agent and marked as delegated; the MCP server started with
              --tasks-only then also serves task_approve. It needs --view too
              (an agent approves only what it can see): allow --view --approve.
              An agent approves at most 10 tasks a call, and never a task that an
              agent created, so "an agent adds a task and approves it" is closed.

Both are off until you turn them on, and only you can: allow and deny refuse any
caller that is an AI agent, so an agent cannot grant itself access, and allow also
refuses to run unless standard input is a terminal. Run them in your own terminal
(deny works from a script too: taking access back is always safe).

Take care with --approve. The text of an Inbox task can come from web pages and other
apps, so it can carry instructions meant for an AI. An agent that may approve can turn
captured text into work that another agent runs unread by you. This is a convenience
boundary against injected text, not a sandbox: an agent with a shell or the database
can bypass it. Leave it off unless you trust the agents on this machine.`,
	}
	cmd.AddCommand(d.newTaskAgentsShowCmd(), d.newTaskAgentsSetCmd(true), d.newTaskAgentsSetCmd(false))
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

func (d Deps) newTaskAgentsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show what AI agents may do with the Inbox of this profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return d.InvalidInput("task agents show takes no arguments (got %q)", cutArg(args[0]))
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				a, err := store.AgentAccess(ctx, p.ID)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeAgentAccess(cmd, p, a)
			})
		},
	}
}

// newTaskAgentsSetCmd is allow (on) or deny (!on). Like the other commands of the operator it
// refuses an agent-driven caller first, before it reads its arguments and before the database is opened.
func (d Deps) newTaskAgentsSetCmd(on bool) *cobra.Command {
	var view, approve bool
	use, short := "deny [--view] [--approve]", "Take back what you delegated to AI agents (you only; no flag takes back both)"
	if on {
		use, short = "allow --view [--approve]", "Let AI agents see the board and the Inbox, or approve Inbox tasks (you only; off by default)"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator("change what AI agents may do with the Inbox")
			if err != nil {
				return err
			}
			if on && !d.StdinIsTerminal() {
				return d.operatorOnlyError("task agents allow needs a terminal on standard input, which this run does not have: run it in your own terminal")
			}
			if len(args) != 0 {
				return d.InvalidInput("task agents takes only the flags --view and --approve (got %q)", cutArg(args[0]))
			}
			if on && !view && !approve {
				return d.InvalidInput("name what to allow: --view (agents see the board and the Inbox), --approve (named agents approve Inbox tasks), or both")
			}
			if !on && !view && !approve {
				view, approve = true, true
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				a, err := store.AgentAccess(ctx, p.ID)
				if err != nil {
					return d.taskErr(err)
				}
				if view {
					a.View = on
				}
				if approve {
					a.Approve = on
				}
				if on && a.Approve && !a.View {
					return d.InvalidInput("--approve needs --view as well (an agent approves only what it can see): run task agents allow --view --approve")
				}
				if err := store.SetAgentAccess(ctx, p.ID, a, actor); err != nil {
					return d.taskErr(err)
				}
				return d.writeAgentAccess(cmd, p, a)
			})
		},
	}
	cmd.Flags().BoolVar(&view, "view", false, "Agents see the whole board and the Inbox (task board, task list, the task_list tool)")
	cmd.Flags().BoolVar(&approve, "approve", false, "Named agents (--as NAME) move Inbox tasks to Ready, recorded as delegated")
	return cmd
}
