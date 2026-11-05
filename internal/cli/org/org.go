package org

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/monomind"
)

// NewCommand is `org`: monomind's org observe/action surface, thin proxies
// over `monomind org <sub> [<name>] --format json` (protocol §7), plus the
// org files, grants, endpoints and autonomy mono-agent keeps. Output is
// always JSON — these commands exist for monoagentcli's own Wails bindings
// and for scripting, not interactive table browsing (mirrors the doctrine
// established by `chat`: the CLI's machine output IS the UI contract).
func NewCommand(d Deps) *cobra.Command {
	var project string
	env := newEnv(d, orgenvFor(d, func() string { return project }))
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Local agent organizations (via the monomind engine)",
		Long: "Observe and act on monomind-managed agent organizations — status, logs, costs, " +
			"pending questions/gates, and a live event tail. Every subcommand prints JSON to " +
			"stdout; there is no human table mode.",
		PersistentPostRun: func(cmd *cobra.Command, args []string) { env.Close() },
	}
	cmd.PersistentFlags().StringVar(&project, "project", "",
		"Project root orgs resolve under (<root>/.monomind/orgs/); default: the folder of the profile named by --profile")
	root := env.root

	cmd.AddCommand(
		newOrgListCmd(root),
		newOrgStatusCmd(root),
		newOrgRunCmd(root),
		newOrgLogsCmd(root),
		newOrgReportCmd(root),
		newOrgCostsCmd(root),
		newOrgBudgetCmd(root),
		newOrgEstimateCmd(root),
		newOrgFlowCmd(root),
		newOrgQuestionsCmd(root),
		newOrgApprovalsCmd(root),
		newOrgGatesCmd(root),
		newOrgDecisionsCmd(root),
		newOrgMemoryCmd(root),
		newOrgAnswerCmd(root),
		newOrgApproveCmd(root, env.refuseSendingApproval),
		newOrgDenyCmd(root),
		newOrgGateApproveCmd(env),
		newOrgGateRejectCmd(root),
		newOrgEventsCmd(root),
		env.newOrgValidateCmd(root),
		newOrgSectionsRuntimesCmd(),
		newOrgReloadCmd(root),
		env.newOrgRoleCmd(env.RootErr),
		newOrgCreateJSONCmd(env),
		newOrgSignCmd(env),
		newOrgAutomationCmd(env),
		newOrgGrantCmd(env),
		newOrgEffectiveToolsCmd(env),
		newOrgLegacyCmd(env),
		newOrgAutonomyCmd(env),
		newOrgSummaryCmd(env),
		newOrgServeCmd(env),
		newOrgLifecycleCmd(env, "stop", "Ask a running org to stop", monomind.OrgStop),
		newOrgLifecycleCmd(env, "pause", "Pause an org: current turns finish, no new cycles start", monomind.OrgPause),
		newOrgLifecycleCmd(env, "resume", "Resume a paused org", monomind.OrgResume),
		newOrgSendCmd(env),
		newOrgChatCmd(env),
		newOrgQueuedCmd(env),
		newOrgDocumentsCmd(env),
		newOrgScheduleAuditCmd(env),
		newOrgRenameCmd(env),
		newOrgDeleteCmd(env),
		newOrgAutomationRoleCmd(env),
		newOrgGroupCmd(env),
		newOrgReconcileCmd(env),
		newOrgReconcileDocCmd(env),
		newOrgTeardownProfileCmd(env),
	)
	requireRoot(cmd, env)
	return cmd
}

// noRootGuard lists the commands requireRoot leaves alone, by command path
// below the root command: ones that need no folder, or that deal with a
// missing one themselves.
var noRootGuard = map[string]bool{
	"org sections-runtimes": true, // needs no folder
	"org summary":           true, // reports local facts without a database (it errors itself when there is no folder)
	"org role set-access":   true, // its operator gate comes first: the root lookup opens the database, so it looks the root up itself, after the gate
	"org teardown-profile":  true, // same; it opens the profile through env.Profile, which reports a database that cannot be opened
}

// requireRoot makes every other org subcommand that acts on a folder fail up
// front when there is none (the database cannot be opened and no --project
// was given), so none of them can run with an empty root, which joins
// relative to the current directory. env.root is only safe to call from
// under it.
func requireRoot(group *cobra.Command, env *orgEnv) { requireRootUnder(group, env, group.Name()) }

func requireRootUnder(group *cobra.Command, env *orgEnv, path string) {
	for _, c := range group.Commands() {
		p := path + " " + c.Name()
		requireRootUnder(c, env, p)
		run := c.RunE
		if run == nil || noRootGuard[p] {
			continue
		}
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if _, err := env.RootErr(); err != nil {
				return err
			}
			return run(cmd, args)
		}
	}
}

// printOrgJSON writes a raw JSON payload to stdout with a trailing newline.
func printOrgJSON(payload []byte) error {
	_, err := fmt.Println(string(payload))
	return err
}

func newOrgListCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all orgs in the project",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgList(cmd.Context(), root())
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgStatusCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Show one org's status, or every org's when name is omitted",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			out, err := monomind.OrgStatus(cmd.Context(), root(), name)
			if err != nil {
				return err
			}
			return printOrgJSON(orgenv.WithSignature(cmd.Context(), root(), out))
		},
	}
}

func newOrgLogsCmd(root func() string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show the org's bus event log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgLogs(cmd.Context(), root(), args[0], run)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: most recent run)")
	return c
}

func newOrgReportCmd(root func() string) *cobra.Command {
	var all bool
	var run string
	c := &cobra.Command{
		Use:   "report <name>",
		Short: "Show the org's run report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgReport(cmd.Context(), root(), args[0], all, run)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "Report on every run instead of just one")
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: most recent run; ignored with --all)")
	return c
}

func newOrgRunCmd(root func() string) *cobra.Command {
	var task string
	var dryRun bool
	c := &cobra.Command{
		Use:   "run <name>",
		Short: "Start an org run (blocks until it completes or hands off to a live serve daemon)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// One run id for this process's lines and monomind's (MONOAGENT_RUN_ID).
			ctx := logging.EnsureRunID(cmd.Context(), "org-")
			out, err := monomind.OrgRun(ctx, root(), args[0], task, dryRun)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&task, "task", "", "Initial message/goal override for this run")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Preview role briefings without starting sessions")
	return c
}

func newOrgCostsCmd(root func() string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "costs <name>",
		Short: "Show per-role token/cost totals",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgCosts(cmd.Context(), root(), args[0], run)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: most recent run)")
	return c
}

func newOrgEstimateCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "estimate <name>",
		Short: "Show monomind's pre-run cost estimate, with its \"stale rates\" note, without starting a run",
		Long: "monomind prints the estimate only at the start of `org run`. This reads it with " +
			"`org run --yes --budget-usd=-1`, which aborts before any session starts, and refuses " +
			"first when an `org serve` daemon is live (any heartbeat age with a live pid), the org is " +
			"unsigned or fails `org validate`, or monomind is older than the tested version (each would " +
			"give no estimate or start a real run). `org run` may reconcile a stale run first, which can " +
			"rewrite the org's runtime.json. The text is monomind's, verbatim.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			est, err := monomind.OrgCostEstimate(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			out, err := json.Marshal(est)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgBudgetCmd(root func() string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "budget <name>",
		Short: "Show per-section spend against allocation, role caps and soft closure",
		Long: "monomind 2.24 reports no per-section spend, so this derives it in one place " +
			"(internal/monomind.SectionBudgets) from the org definition and the run's usage and " +
			"section-budget audit events. The org total equals the sum of the usage events' cost_usd, " +
			"the figure `org report` prints.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := monomind.OrgBudget(cmd.Context(), root(), args[0], run)
			if err != nil {
				return err
			}
			out, err := json.Marshal(rep)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: current run)")
	return c
}

func newOrgFlowCmd(root func() string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "flow <name>",
		Short: "Show the org's role communication graph",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgFlow(cmd.Context(), root(), args[0], run)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: most recent run)")
	return c
}

func newOrgQuestionsCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "questions <name>",
		Short: "List pending human-input questions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgQuestions(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgApprovalsCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "approvals <name>",
		Short: "List pending tool/action approval requests",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgApprovals(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgGatesCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "gates <name>",
		Short: "List pending decision gates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgGates(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgDecisionsCmd(root func() string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "decisions <name>",
		Short: "Show the org's decision trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgDecisions(cmd.Context(), root(), args[0], run)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: most recent run)")
	return c
}

func newOrgMemoryCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "memory <name>",
		Short: "Show org memory statistics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgMemoryStats(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgAnswerCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "answer <name> <questionId> <answer...>",
		Short: "Answer a pending human-input question",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			answer := joinRemainder(args[2:])
			out, err := monomind.OrgAnswer(cmd.Context(), root(), args[0], args[1], answer)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgApproveCmd(root func() string, refuse func(ctx context.Context, org, role, action string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "approve <name> <role> <action>",
		Short: "Approve a pending tool-approval request",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuse(cmd.Context(), args[0], args[1], args[2]); err != nil {
				return err
			}
			out, err := monomind.OrgApprove(cmd.Context(), root(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgDenyCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "deny <name> <role> <action>",
		Short: "Deny a pending tool-approval request",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgDeny(cmd.Context(), root(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgGateApproveCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "gate-approve <name> <gateId> [resolution...]",
		Short: "Approve a decision gate (operator only)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Approving a gate is approving an approval: the user's (class B).
			if err := env.refuseNotOperator(fmt.Sprintf("approving %s in org %s", args[1], args[0])); err != nil {
				return err
			}
			resolution := joinRemainder(args[2:])
			out, err := monomind.OrgGateApprove(cmd.Context(), env.root(), args[0], args[1], resolution)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgGateRejectCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "gate-reject <name> <gateId> [resolution...]",
		Short: "Reject a decision gate",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolution := joinRemainder(args[2:])
			out, err := monomind.OrgGateReject(cmd.Context(), root(), args[0], args[1], resolution)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgEventsCmd(root func() string) *cobra.Command {
	var run, since string
	var follow bool
	c := &cobra.Command{
		Use:   "events <name>",
		Short: "Stream the org's bus event log as NDJSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A reader that went away (the app quit) must end the tail, not
			// kill this process with SIGPIPE before it stops monomind: with
			// SIGPIPE ignored the write fails with EPIPE, which cancels.
			signal.Ignore(syscall.SIGPIPE)
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			w := &eventsWriter{w: os.Stdout, cancel: cancel}
			err := monomind.OrgEvents(ctx, root(), args[0], monomind.OrgEventsOptions{
				Run:    run,
				Follow: follow,
				Since:  since,
			}, w.writeLine)
			if w.err != nil {
				return w.err
			}
			return err
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: current run)")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "Keep streaming as new events arrive")
	c.Flags().StringVar(&since, "since", "", "Replay from a cursor (event id or ISO timestamp)")
	return c
}

// eventsWriter prints `org events` lines and cancels the tail on the first
// failed write, so monomind's follower is stopped instead of left writing
// to nobody (monoes/mono-agent#235).
type eventsWriter struct {
	w      io.Writer
	cancel context.CancelFunc
	err    error
}

func (e *eventsWriter) writeLine(line []byte) {
	if e.err != nil {
		return
	}
	if _, err := e.w.Write(append(line, '\n')); err != nil {
		e.err = fmt.Errorf("write events: %w", err)
		e.cancel()
	}
}

// joinRemainder joins trailing positional args with a space, mirroring
// monomind's own answer/gate-approve/gate-reject argument handling.
func joinRemainder(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
