package task

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// boundSessionEnv names the session an agent's process is bound to: the carrier of the
// session a worker's token will hold. An agent that sets it may act only in that session.
const boundSessionEnv = "MONOAGENT_TASK_SESSION"

// boundNumber reads a binding variable: 0 when it is blank, the number when it is one, and -1
// (a binding to nothing that exists, which refuses every act) when it is set to anything else:
// a garbled worker environment is not an unbound process.
func boundNumber(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// boundSession reads boundSessionEnv. A task without its session is a damaged
// binding (-1), not an unbound Personal-board caller.
func (d Deps) boundSession() int64 {
	session := boundNumber(d.Getenv(boundSessionEnv))
	if session == 0 && d.boundTask() != 0 {
		return -1
	}
	return session
}

// boundTaskEnv narrows boundSessionEnv to one task: what a mission worker's process holds.
const boundTaskEnv = "MONOAGENT_TASK_ID"

// boundTask reads boundTaskEnv: 0 when it is not set.
func (d Deps) boundTask() int64 { return boundNumber(d.Getenv(boundTaskEnv)) }

// boundRoleEnv narrows the task binding to a verifier's: "verifier" lets the process verify,
// comment on and attach evidence to its one task and nothing else.
const boundRoleEnv = "MONOAGENT_TASK_ROLE"

func (d Deps) boundVerifier() bool {
	return d.boundTask() != 0 && strings.TrimSpace(d.Getenv(boundRoleEnv)) == "verifier"
}

// parseSessionID reads 3 or #3.
func (d Deps) parseSessionID(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(s), "#"), 10, 64)
	if err != nil || n <= 0 {
		return 0, d.InvalidInput("%q is not a session id (write 3 or #3)", cutArg(s))
	}
	return n, nil
}

// sessionFlag reads --session, which must name a session when it is given.
func (d Deps) sessionFlag(cmd *cobra.Command, value string) (int64, bool, error) {
	if !cmd.Flags().Changed("session") {
		return 0, false, nil
	}
	id, err := d.parseSessionID(value)
	if err != nil {
		return 0, false, d.InvalidInput("--session: %v", err)
	}
	return id, true, nil
}

// actorFor judges who runs a verb that the operator and a named agent may both do.
func (d Deps) actorFor(cmd *cobra.Command, what string) (tasks.Actor, error) {
	caller := d.callerFor(flagAs(cmd))
	if caller.isAgent() {
		return d.namedAgent(caller)
	}
	return caller.operator(what)
}

func (d Deps) writeSession(cmd *cobra.Command, p tasks.Profile, se tasks.Session, verb string) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "session": se})
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	fmt.Fprintf(w, "%s session #%d [%s]: %s\n", verb, se.ID, se.State, taskCut(se.Title, 70))
	return nil
}

func money(f float64) string { return fmt.Sprintf("$%.2f", f) }

// printSession prints a session: its state, limits, progress and goal (the goal is text an
// agent may have written, so it is printed as the notes of a task are).
func printSession(w io.Writer, p tasks.Profile, se tasks.Session) {
	fmt.Fprintf(w, "Session #%d [%s]  %s\n", se.ID, se.State, se.Title)
	fmt.Fprintf(w, "Profile:  %s\n", p.Name)
	if se.Archived {
		fmt.Fprintln(w, "Archived: yes")
	}
	c := se.Counts
	fmt.Fprintf(w, "Progress: %d/%d done (inbox %d, ready %d, in progress %d, review %d; blocked %d, verified %d)\n",
		c.Done, c.Total, c.Inbox, c.Ready, c.InProgress, c.Review, c.Blocked, c.Verified)
	budget := "no budget set"
	if se.BudgetUSD > 0 {
		budget = "of " + money(se.BudgetUSD)
	}
	fmt.Fprintf(w, "Spent:    %s %s\n", money(se.SpentUSD), budget)
	fmt.Fprintf(w, "Limits:   %d tasks, depth %d, %d retries; access %s; auto-close verified: %s\n",
		se.MaxTasks, se.MaxDepth, se.MaxRetries, se.AccessLevel, onOff(se.AutoCloseVerified))
	fmt.Fprintf(w, "Created:  %s by %s\n", se.CreatedAt.Local().Format("2006-01-02 15:04"), se.CreatedBy)
	if se.ApprovedAt != nil {
		fmt.Fprintf(w, "Approved: %s by %s\n", se.ApprovedAt.Local().Format("2006-01-02 15:04"), se.ApprovedBy)
	}
	if se.ConversationID != "" {
		fmt.Fprintf(w, "Chat:     %s\n", se.ConversationID)
	}
	printNotes(w, se.Goal)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// printSessionEvents prints a session's history; the notes are written by people and agents.
func printSessionEvents(w io.Writer, events []tasks.SessionEvent) {
	if len(events) == 0 {
		return
	}
	fmt.Fprintln(w, "\nHistory:")
	for _, e := range events {
		fmt.Fprintf(w, "  %s  %-12s %s\n", e.At.Local().Format("01-02 15:04"), e.Actor, e.Kind)
		if e.Note != "" {
			fmt.Fprintf(w, "      %s\n", taskCut(strings.ReplaceAll(e.Note, "\n", " "), 200))
		}
	}
}

// printPlan prints a session's tasks with what each waits for and how it is judged.
func printPlan(w io.Writer, p tasks.Profile, pl tasks.SessionPlan) {
	printSession(w, p, pl.Session)
	fmt.Fprintln(w, "\nPlan:")
	if len(pl.Tasks) == 0 {
		fmt.Fprintln(w, "  No tasks yet.")
		return
	}
	for _, t := range pl.Tasks {
		flags := string(t.Status)
		if t.Blocked {
			flags += ", blocked"
		}
		if t.Verified {
			flags += ", verified"
		}
		line := fmt.Sprintf("  #%d [%s] %s: %s", t.ID, flags, t.Kind, taskCut(t.Title, 70))
		if t.ParentID != 0 {
			line += fmt.Sprintf("  (under #%d)", t.ParentID)
		}
		if len(t.DependsOn) > 0 {
			line += "  (after " + joinIDs(t.DependsOn) + ")"
		}
		if h := heldNote(t); h != "" {
			line += "  [" + h + "]"
		}
		fmt.Fprintln(w, line)
		fmt.Fprintf(w, "      done when: %s\n", taskCut(strings.ReplaceAll(t.Acceptance, "\n", " "), 160))
	}
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "#" + strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

// newTaskSessionCmd is `task session`: the sessions of a profile's board.
func (d Deps) newTaskSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Task sessions: one goal with its own tasks, planned by an agent, opened by you",
		Long: `A session is one goal with a board of its own. An agent (or you) plans it as a
draft: its tasks cannot be taken until you approve the plan. After approval AI
agents work inside the session only: they add follow-ups, claim, comment, finish,
split, attach artifacts and verify, and nothing outside it. A verifier marks a
task verified; closing it to Done stays yours unless you turned on auto-close.

Only you approve, resume, set the budget, the limits, the access level and
auto-close, and archive. Pausing and stopping are open to agents; stop releases
every claim. Add tasks with task add --session N --acceptance TEXT. See: monoagentcli ref tasks`,
	}
	cmd.AddCommand(
		d.newSessionNewCmd(),
		d.newSessionListCmd(),
		d.newSessionShowCmd(),
		d.newSessionPlanCmd(),
		d.newSessionApproveCmd(),
		d.newSessionLifecycleCmd("pause", "Stop a running session handing out tasks (you or an agent)", true,
			func(s *tasks.Store, ctx context.Context, p string, id int64, a tasks.Actor) (tasks.Session, error) {
				return s.PauseSession(ctx, p, id, a)
			}),
		d.newSessionLifecycleCmd("resume", "Run a paused session again (you only)", false,
			func(s *tasks.Store, ctx context.Context, p string, id int64, a tasks.Actor) (tasks.Session, error) {
				return s.ResumeSession(ctx, p, id, a)
			}),
		d.newSessionLifecycleCmd("stop", "Stop a session for good and release every claim (you or an agent)", true,
			func(s *tasks.Store, ctx context.Context, p string, id int64, a tasks.Actor) (tasks.Session, error) {
				return s.StopSession(ctx, p, id, a)
			}),
		d.newSessionLifecycleCmd("archive", "Hide a session that is not running (you only)", false,
			func(s *tasks.Store, ctx context.Context, p string, id int64, a tasks.Actor) (tasks.Session, error) {
				return s.ArchiveSession(ctx, p, id, a)
			}),
		d.newSessionBudgetCmd(),
	)
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

func (d Deps) newSessionNewCmd() *cobra.Command {
	var goal, access, conversation, clientID string
	var budget float64
	var maxTasks, maxDepth int
	cmd := &cobra.Command{
		Use:   "new TITLE... [--goal TEXT] [--conversation ID] [--client-id KEY] [--budget-usd N --max-tasks N --max-depth N --access LEVEL]",
		Short: "Start a session, as a draft (an agent plans; the budget, limits and access are yours to set)",
		RunE: func(cmd *cobra.Command, args []string) error {
			caller := d.callerFor(flagAs(cmd))
			actor := d.author(caller)
			if actor.Kind == tasks.Agent {
				var err error
				if actor, err = d.namedAgent(Caller{d: d, actor: actor, marker: caller.marker, asBlank: caller.asBlank, envBlank: caller.envBlank}); err != nil {
					return err
				}
			}
			in := tasks.SessionInput{
				Title: strings.Join(args, " "), Goal: goal, ConversationID: conversation,
				AccessLevel: access, BudgetUSD: budget, MaxTasks: maxTasks, MaxDepth: maxDepth, ClientID: clientID,
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				se, err := store.CreateSession(ctx, p.ID, in, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeSession(cmd, p, se, "Started")
			})
		},
	}
	cmd.Flags().StringVar(&goal, "goal", "", "What the session is for (the plan card shows it)")
	cmd.Flags().StringVar(&conversation, "conversation", "", "The chat conversation the session belongs to")
	cmd.Flags().StringVar(&clientID, "client-id", "", "An idempotency key (up to 64 letters, digits, - or _): a repeat in the profile returns the same session, with deduped true")
	cmd.Flags().Float64Var(&budget, "budget-usd", 0, "Budget in dollars (you only; 0 for none)")
	cmd.Flags().IntVar(&maxTasks, "max-tasks", 0, "Most tasks the session holds (you only; default 200)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "Deepest nesting of tasks (you only; default 3)")
	cmd.Flags().StringVar(&access, "access", "", "Access level of its workers: scoped, coding, workspace or full (you only; default scoped)")
	return cmd
}

func (d Deps) newSessionListCmd() *cobra.Command {
	var all bool
	var state string
	cmd := &cobra.Command{
		Use:   "list [--state S] [--all]",
		Short: "List the profile's sessions, newest first (archived ones with --all)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return d.InvalidInput("task session list takes no arguments (got %q)", cutArg(args[0]))
			}
			actor := d.callerFor(flagAs(cmd)).actor
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				ss, err := store.ListSessions(ctx, p.ID, tasks.SessionFilter{State: tasks.SessionState(state), IncludeArchived: all}, actor)
				if err != nil {
					return d.taskErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "sessions": ss})
				}
				w := cmd.OutOrStdout()
				printProfileLine(w, p)
				if len(ss) == 0 {
					fmt.Fprintln(w, "No sessions.")
					return nil
				}
				for _, se := range ss {
					fmt.Fprintf(w, "#%d  %-8s %d/%d done  %s of %s  %s\n", se.ID, se.State, se.Counts.Done, se.Counts.Total,
						money(se.SpentUSD), budgetWord(se), taskCut(se.Title, 60))
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include archived sessions")
	cmd.Flags().StringVar(&state, "state", "", "Only sessions in this state (draft, running, paused, done, stopped, failed)")
	return cmd
}

func budgetWord(se tasks.Session) string {
	if se.BudgetUSD > 0 {
		return money(se.BudgetUSD)
	}
	return "no budget"
}

// oneSession is the shape of the commands that take a session id: it parses the argument
// before the database is opened.
func (d Deps) oneSession(use, short string, run func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return d.InvalidInput("task session %s takes one session id, written 3 or #3 (got %d arguments)", strings.Fields(use)[0], len(args))
			}
			id, err := d.parseSessionID(args[0])
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				return run(cmd, store, p, id)
			})
		},
	}
}

func (d Deps) newSessionShowCmd() *cobra.Command {
	var events int
	cmd := d.oneSession("show ID [--events N]", "Show a session: its state, limits, progress, goal and history",
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			actor := d.callerFor(flagAs(cmd)).actor
			se, err := store.GetSession(cmd.Context(), p.ID, id, actor)
			if err != nil {
				return d.taskErr(err)
			}
			evs, err := store.SessionEvents(cmd.Context(), p.ID, id, events, actor)
			if err != nil {
				return d.taskErr(err)
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "session": se, "events": evs})
			}
			printSession(cmd.OutOrStdout(), p, se)
			printSessionEvents(cmd.OutOrStdout(), evs)
			return nil
		})
	cmd.Flags().IntVar(&events, "events", 50, "Show the newest N events of its history (0 for all)")
	return cmd
}

func (d Deps) newSessionPlanCmd() *cobra.Command {
	return d.oneSession("plan ID", "Show a session's tasks with their dependencies and acceptance criteria",
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			pl, err := store.PlanOfSession(cmd.Context(), p.ID, id, d.callerFor(flagAs(cmd)).actor)
			if err != nil {
				return d.taskErr(err)
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "session": pl.Session, "tasks": pl.Tasks})
			}
			printPlan(cmd.OutOrStdout(), p, pl)
			return nil
		})
}

func (d Deps) newSessionApproveCmd() *cobra.Command {
	var actor tasks.Actor
	return d.operatorSession("approve ID", "Approve a draft's plan: its tasks become Ready and the session runs (you only)", "approve a session's plan", &actor,
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			se, err := store.ApproveSession(cmd.Context(), p.ID, id, actor)
			if err != nil {
				return d.taskErr(err)
			}
			return d.writeSession(cmd, p, se, "Approved")
		})
}

// operatorSession is oneSession for a command that is the operator's: it refuses an
// agent-driven caller first, before it reads its argument or opens the database.
func (d Deps) operatorSession(use, short, what string, actor *tasks.Actor, run func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error) *cobra.Command {
	cmd := d.oneSession(use, short, run)
	inner := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		a, err := d.callerFor(flagAs(c)).operator(what)
		if err != nil {
			return err
		}
		*actor = a
		return inner(c, args)
	}
	return cmd
}

var pastTense = map[string]string{"pause": "Paused", "resume": "Resumed", "stop": "Stopped", "archive": "Archived"}

// newSessionLifecycleCmd is pause, resume, stop or archive. open says an agent may too.
func (d Deps) newSessionLifecycleCmd(verb, short string, open bool, do func(*tasks.Store, context.Context, string, int64, tasks.Actor) (tasks.Session, error)) *cobra.Command {
	var actor tasks.Actor
	run := func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
		se, err := do(store, cmd.Context(), p.ID, id, actor)
		if err != nil {
			return d.taskErr(err)
		}
		return d.writeSession(cmd, p, se, pastTense[verb])
	}
	if !open {
		return d.operatorSession(verb+" ID", short, verb+" a session", &actor, run)
	}
	cmd := d.oneSession(verb+" ID", short, run)
	inner := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		a, err := d.actorFor(c, verb+" a session")
		if err != nil {
			return err
		}
		actor = a
		return inner(c, args)
	}
	return cmd
}

func (d Deps) newSessionBudgetCmd() *cobra.Command {
	var usd, spend float64
	var maxTasks, maxDepth, maxRetries int
	var access, autoClose, note string
	var actor tasks.Actor
	cmd := d.operatorSession("budget ID", "Set a session's budget, limits, access level or auto-close, or record spend (you only)", "change a session's budget or limits", &actor,
		func(cmd *cobra.Command, store *tasks.Store, p tasks.Profile, id int64) error {
			var u tasks.SessionUpdate
			f := cmd.Flags()
			if f.Changed("usd") {
				u.BudgetUSD = &usd
			}
			if f.Changed("max-tasks") {
				u.MaxTasks = &maxTasks
			}
			if f.Changed("max-depth") {
				u.MaxDepth = &maxDepth
			}
			if f.Changed("max-retries") {
				u.MaxRetries = &maxRetries
			}
			if f.Changed("access") {
				u.AccessLevel = &access
			}
			if f.Changed("auto-close") {
				switch autoClose {
				case "on", "off":
					on := autoClose == "on"
					u.AutoCloseVerified = &on
				default:
					return d.InvalidInput("--auto-close is on or off")
				}
			}
			if u == (tasks.SessionUpdate{}) && !f.Changed("spend") {
				return d.InvalidInput("nothing to change: give --usd, --max-tasks, --max-depth, --max-retries, --access, --auto-close or --spend")
			}
			if f.Changed("spend") && (spend <= 0 || math.IsNaN(spend) || math.IsInf(spend, 0)) {
				return d.InvalidInput("--spend is an amount in dollars above 0")
			}
			if !f.Changed("spend") {
				spend = 0
			}
			// The limits and the spend are one write: a refusal of either changes neither.
			se, err := store.ChangeSession(cmd.Context(), p.ID, id, u, spend, note, actor)
			if err != nil {
				return d.taskErr(err)
			}
			return d.writeSession(cmd, p, se, "Updated")
		})
	cmd.Flags().Float64Var(&usd, "usd", 0, "Budget in dollars (0 for none)")
	cmd.Flags().IntVar(&maxTasks, "max-tasks", 0, "Most tasks the session holds")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "Deepest nesting of tasks")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 0, "Failed verifications a task may have before it is blocked")
	cmd.Flags().StringVar(&access, "access", "", "Access level of its workers: scoped, coding, workspace or full")
	cmd.Flags().StringVar(&autoClose, "auto-close", "", "on: a verifier's pass closes the task to Done; off (the default): only you close it")
	cmd.Flags().Float64Var(&spend, "spend", 0, "Record this much more spent, in dollars")
	cmd.Flags().StringVar(&note, "note", "", "What the spend was for")
	return cmd
}
