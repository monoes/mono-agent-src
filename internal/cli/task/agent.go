package task

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The agent's commands. Each one that needs the agent's name judges the caller first, before it reads
// its arguments and before it opens the database (the operator's commands refuse an agent the same
// way), then checks its own arguments, so that a mistake is exit 3 with the --json error document that
// cobra's Args validators would not give (they answer exit 1 and no document). The text they print
// carries what the task says only as the notes block of printNotes, and every command an agent is told
// to run next is written by taskCommand, so that it names the profile and, where it is the agent's own, the agent.

// agentNameError says why name is not a name an agent may act under, or is nil: in the store's own words
// (tasks.CheckAgentName, the rule the store applies itself) and with how to choose another. The store judges
// a name only once the database is open; judged here, a name that cannot be accepted is refused before
// anything is opened.
func (d Deps) agentNameError(name string) error {
	if err := tasks.CheckAgentName(name); err != nil {
		return d.taskErr(fmt.Errorf("%w (choose another name with --as NAME)", err))
	}
	return nil
}

// namedAgent is the agent that runs a command of the agent's: one that says its name (callerFor's
// agent), with a name the store takes.
func (d Deps) namedAgent(caller Caller) (tasks.Actor, error) {
	actor, err := caller.agent()
	if err != nil {
		return tasks.Actor{}, err
	}
	if err := d.agentNameError(actor.Name); err != nil {
		return tasks.Actor{}, err
	}
	return actor, nil
}

// takeCommand is a command that takes a task, as a printer suggests it. as is the name of the agent that
// reads it: the command carries that name (taskCommand writes it, as <name> when a shell would not read it as
// one word), and the place for a name when the reader has none, the operator or an agent that has not said who
// it is, so that the reader is the one to choose it: pasted without --as NAME the command would run as the operator.
func takeCommand(p tasks.Profile, as, words string) string {
	if as == "" {
		return taskCommand(p, "", words) + " --as <your-name>"
	}
	return taskCommand(p, as, words)
}

// continueHelp tells an agent how to carry on with a task it holds, in four commands that carry the profile
// (the active profile can change under a session) and the agent's name, both as taskCommand writes them.
func continueHelp(w io.Writer, p tasks.Profile, t tasks.Task, name string) {
	comment := taskCommand(p, name, fmt.Sprintf("comment %d", t.ID))
	finish := taskCommand(p, name, fmt.Sprintf("finish %d", t.ID))
	release := taskCommand(p, name, fmt.Sprintf("release %d", t.ID))
	fmt.Fprintf(w, "\nWork it, then hand it back. Use the same name (%s) for every call:\n", name)
	fmt.Fprintf(w, "  report progress   %s \"what you did\"\n", comment)
	fmt.Fprintf(w, "  done              %s --result \"what you did\"\n", finish)
	fmt.Fprintf(w, "  need an answer    %s --question \"what you need to know\"\n", finish)
	fmt.Fprintf(w, "  give it back      %s --note \"why\"\n", release)
}

// printClaimed prints a task an agent has just claimed.
func printClaimed(w io.Writer, p tasks.Profile, t tasks.Task, name string) {
	fmt.Fprintf(w, "Claimed #%d (%s): %s\n", t.ID, heldNote(t), t.Title)
	if t.Source.URL != "" {
		fmt.Fprintf(w, "Link: %s\n", t.Source.URL)
	}
	printNotes(w, t.Notes) // Task 9's helper: the notice, the notes indented, an end line
	printAcceptance(w, t)
	continueHelp(w, p, t, name)
}

// printNext prints the task an agent would take, and how to take it. as is the name of the agent that
// looks, "" for a caller that has none (see takeCommand); session is the session it is asked of (0: the personal board).
func printNext(w io.Writer, p tasks.Profile, t tasks.Task, as string, session int64) {
	fmt.Fprintf(w, "Next task: #%d %s   [%s, from %s]\n", t.ID, t.Title, t.Status, t.Source.Kind)
	if t.Source.URL != "" {
		fmt.Fprintf(w, "Link: %s\n", t.Source.URL)
	}
	printNotes(w, t.Notes) // Task 9's helper: the notice, the notes indented, an end line
	printAcceptance(w, t)
	next := "next --claim"
	if session != 0 { // a session's next is asked of that session
		next = fmt.Sprintf("next --session %d --claim", session)
	}
	fmt.Fprintf(w, "\nTake it:\n  %s\n  %s\n", takeCommand(p, as, next), takeCommand(p, as, fmt.Sprintf("claim %d", t.ID)))
}

// checkLease refuses a --lease that was given and is not a positive time. The store reads zero and
// less as the default, which is for a caller that gave none: a lease that was typed means what it says
// (--lease "$TIME", TIME unset, or a minus sign by mistake), and an agent that is not told learns nothing.
func (d Deps) checkLease(cmd *cobra.Command, lease time.Duration) error {
	if cmd.Flags().Changed("lease") && lease <= 0 {
		return d.InvalidInput("--lease must be a positive time such as 30m; the default is 30m and the most is 24h")
	}
	return nil
}

func (d Deps) newTaskNextCmd() *cobra.Command {
	var claim bool
	var lease time.Duration
	var session string
	cmd := &cobra.Command{
		Use:   "next [--claim --as NAME [--lease 30m]]",
		Short: "Show the task an AI agent should work next, or claim it",
		Long: `The task is the top of Ready (an approved task), else a claim whose lease has run
out. Without --claim it only looks: two agents that look may see the same task.
With --claim and --as NAME it takes the task for you in one step, so two agents
never get the same one. A claim lasts 30 minutes by default (--lease, at most
24h); a comment of yours extends it to 30 minutes from the comment, if that is
later, and never shortens it. Nothing ready: the text says so, and --json gives
a null task.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			caller := d.callerFor(flagAs(cmd))
			actor := caller.actor
			if claim {
				var err error
				if actor, err = d.namedAgent(caller); err != nil {
					return err
				}
			}
			if len(args) != 0 {
				return d.InvalidInput("task next takes no arguments (got %q): its options are --claim and --lease", cutArg(args[0]))
			}
			if err := d.checkLease(cmd, lease); err != nil {
				return err
			}
			if !claim && cmd.Flags().Changed("lease") {
				return d.InvalidInput("--lease only applies with --claim: a look at the next task takes no lease")
			}
			if sid, ok, err := d.sessionFlag(cmd, session); err != nil {
				return err
			} else if ok {
				return d.runSessionNext(cmd, caller, actor, sid, claim, lease)
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.Next(ctx, p.ID, actor, claim, lease)
				if err != nil {
					return d.taskErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "task": t})
				}
				w := cmd.OutOrStdout()
				printProfileLine(w, p)
				switch {
				case t == nil:
					fmt.Fprintln(w, "Nothing is ready.")
				case claim:
					printClaimed(w, p, *t, actor.Name)
				default:
					printNext(w, p, *t, caller.actor.Name, 0)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Work this session's tasks instead of the personal board: a Ready task whose dependencies are done, of a running session")
	cmd.Flags().BoolVar(&claim, "claim", false, "Take the task for yourself (needs --as NAME)")
	cmd.Flags().DurationVar(&lease, "lease", 0, "How long you hold it (default 30m, at most 24h); a comment extends it to 30 minutes from the comment, if that is later")
	return cmd
}

func (d Deps) newTaskClaimCmd() *cobra.Command {
	var lease time.Duration
	cmd := &cobra.Command{
		Use:   "claim ID --as NAME [--lease 30m]",
		Short: "Take a ready task (an AI agent), or renew your hold on one",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.namedAgent(d.callerFor(flagAs(cmd)))
			if err != nil {
				return err
			}
			if len(args) != 1 {
				return d.InvalidInput("task claim takes one task id, written 42 or #42 (got %d arguments)", len(args))
			}
			taskID, err := d.parseTaskID(args[0])
			if err != nil {
				return err
			}
			if err := d.checkLease(cmd, lease); err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.ClaimAny(ctx, p.ID, taskID, actor, lease)
				if err != nil {
					return d.taskErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "task": t})
				}
				printProfileLine(cmd.OutOrStdout(), p)
				printClaimed(cmd.OutOrStdout(), p, t, actor.Name)
				return nil
			})
		},
	}
	cmd.Flags().DurationVar(&lease, "lease", 0, "How long you hold it (default 30m, at most 24h)")
	return cmd
}

func (d Deps) newTaskCommentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "comment ID TEXT... [--as NAME]",
		Short: "Add a note to a task; an agent's note extends its lease to 30 minutes from now, if later",
		RunE: func(cmd *cobra.Command, args []string) error {
			// The comment is the operator's only when nothing says that an agent runs the command:
			// not a marker, not --as (a blank one too), not MONOAGENT_ACTOR. An agent's comment is its own.
			caller := d.callerFor(flagAs(cmd))
			actor := caller.actor
			if caller.isAgent() {
				var err error
				if actor, err = d.namedAgent(caller); err != nil {
					return err
				}
			}
			if len(args) < 2 {
				return d.InvalidInput("task comment takes a task id and the text of the comment, as in task comment 42 \"what you did\" (got %d arguments)", len(args))
			}
			taskID, err := d.parseTaskID(args[0])
			if err != nil {
				return err
			}
			text := strings.Join(args[1:], " ")
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.CommentAny(ctx, p.ID, taskID, text, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeOneTask(cmd, p, t, "Noted")
			})
		},
	}
}

func (d Deps) newTaskFinishCmd() *cobra.Command {
	var result, question string
	cmd := &cobra.Command{
		Use:   "finish ID --as NAME (--result TEXT | --question TEXT)",
		Short: "Hand a task you hold back for review, with a result or a question",
		Long: `The task goes to Review for the operator to read. Give exactly one of --result
(what you did) or --question (what you need to know before you can go on).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.namedAgent(d.callerFor(flagAs(cmd)))
			if err != nil {
				return err
			}
			if len(args) != 1 {
				return d.InvalidInput("task finish takes one task id, written 42 or #42 (got %d arguments): what you did goes in --result, what you need to know in --question", len(args))
			}
			taskID, err := d.parseTaskID(args[0])
			if err != nil {
				return err
			}
			// The flags that were given count, not their text: --result "" --question "q" is two, however the store
			// would read the text. (A single flag with no text is the store's to refuse.)
			if cmd.Flags().Changed("result") == cmd.Flags().Changed("question") {
				return d.InvalidInput("give exactly one of --result TEXT (what you did) and --question TEXT (what you need to know before you can go on)")
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.FinishAny(ctx, p.ID, taskID, tasks.Outcome{Result: result, Question: question}, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeOneTask(cmd, p, t, "Handed back")
			})
		},
	}
	cmd.Flags().StringVar(&result, "result", "", "What you did")
	cmd.Flags().StringVar(&question, "question", "", "What you need to know before you can go on")
	return cmd
}

func (d Deps) newTaskReleaseCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "release ID --as NAME [--note TEXT]",
		Short: "Give a task you hold back to Ready, behind the others",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.namedAgent(d.callerFor(flagAs(cmd)))
			if err != nil {
				return err
			}
			if len(args) != 1 {
				return d.InvalidInput("task release takes one task id, written 42 or #42 (got %d arguments): the reason goes in --note", len(args))
			}
			taskID, err := d.parseTaskID(args[0])
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				t, err := store.ReleaseAny(ctx, p.ID, taskID, note, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeOneTask(cmd, p, t, "Released")
			})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "Why you are giving it back")
	return cmd
}

func (d Deps) newTaskDigestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "digest",
		Short: "A line for a session-start hook (in text, prints nothing when there are none)",
		Long: `In text, prints nothing when the profile has no ready task, otherwise two short
lines: the counts, the next task and the command to take it. With --json it prints
its document on success, even when nothing is ready. It always exits 0 whatever goes
wrong at run time (a failure is one line on standard error), so a hook can call it:
monoagentcli --profile <id> task digest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A hook must never fail on a digest: what goes wrong, a mistake in the arguments too, is
			// one line on standard error, and nothing is printed on standard output.
			if len(args) != 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "task digest: takes no arguments (got %q)\n", cutArg(args[0]))
				return nil
			}
			// A worker bound to a session or a task reads nothing of the personal board: no counts, no title.
			binding := d.callerFor(flagAs(cmd)).actor
			look := tasks.Actor{Kind: tasks.Agent, Session: binding.Session, Task: binding.Task, Verifier: binding.Verifier}
			err := d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				var c tasks.Counts
				var err error
				if look.Session == 0 && look.Task == 0 {
					if c, err = store.Counts(ctx, p.ID); err != nil {
						return err
					}
				}
				var next *tasks.Task
				if c.Ready > 0 {
					if next, err = store.Next(ctx, p.ID, look, false, 0); err != nil {
						return err
					}
				}
				if d.JSONOutput() {
					var n any
					if next != nil {
						n = map[string]any{"id": next.ID, "title": next.Title}
					}
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "ready": c.Ready, "next": n})
				}
				if next == nil {
					return nil
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "MonoAgent task board (%s): %d ready, %d in progress, %d to review. Next: #%d %s\n",
					p.Name, c.Ready, c.InProgress, c.Review, next.ID, taskCut(next.Title, 80))
				fmt.Fprintf(w, "Take it with: %s\n", takeCommand(p, "", "next --claim"))
				return nil
			})
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "task digest: %v\n", err)
			}
			return nil
		},
	}
}
