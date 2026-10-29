package chat

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions mission-children`: the person's control of a mission's child sessions (phase P-C of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md). Both verbs are the operator's: a process that
// holds a coder session token or a mission binding is refused here and never falls back to anything else, so
// the lead cannot turn on mode all, lift a cap or reach these numbers through the shell. The lead has its own
// tools (mission_child_mark, mission_child_ceiling, mission_child_status) with the narrower rules.

func newCoderSessionsMissionChildrenCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mission-children",
		Short: "Set or show how a mission's tasks may run in sessions of their own (the operator's)",
		Long: "A mission's lead may run some of its tasks in coder sessions of their own. The policy has a mode (off, marked: only " +
			"tasks the lead marked, all: every worker task; all is turned on only here), how many may run at once and what " +
			"share of the lead's remaining budget they may carve. An operator who writes a number also sets it as the lead's " +
			"cap, so the lead may lower it but not raise it past it; giving the lead a value without limiting takes two calls " +
			"(set it, then clear the cap with --clear-max-children-cap or --clear-share-pct-cap). An operator off stays off " +
			"until you turn the mode on. None of it starts a session until you run `enable`: child sessions are OFF on a fresh install " +
			"and after an upgrade. `disable` stops new sessions only; sessions already running finish and are settled and charged as usual.",
	}
	cmd.AddCommand(newMissionChildrenSetCmd(cfg), newMissionChildrenListCmd(cfg), newMissionChildrenSwitchCmd(cfg, true), newMissionChildrenSwitchCmd(cfg, false), newMissionChildrenResolveChargeCmd(cfg))
	return cmd
}

// requireMissionOperator refuses any process that holds a coder session token or a task binding, then asks
// the CLI's operator check.
func requireMissionOperator(cfg *Deps, what string) error {
	if sessionTokenFromEnv() != "" || os.Getenv(agenttokens.EnvMissionToken) != "" {
		return clicore.OperatorOnly("only the user can %s: this process runs inside a coder session or a mission task", what)
	}
	return clicore.RequireOperator(cfg.RequireOperator, what)
}

func newMissionChildrenSetCmd(cfg *Deps) *cobra.Command {
	var mode string
	var maxChildren, sharePct int
	var clearMax, clearShare bool
	cmd := &cobra.Command{
		Use:   "set LEAD_CONVERSATION",
		Short: "Change the child-session policy of the mission a conversation leads",
		Long: "Changes only the flags you give. --mode off|marked|all; --max-children N (at most the lead's own max_children setting); " +
			"--share-pct P (at most 80). An operator who writes a number also sets it as the lead's cap; giving the lead a value " +
			"without limiting takes two calls: set the number, then --clear-max-children-cap / --clear-share-pct-cap.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMissionOperator(cfg, "change a mission's child-session policy"); err != nil {
				return err
			}
			var patch coderhost.PolicyPatch
			f := cmd.Flags()
			if f.Changed("mode") {
				patch.Mode = &mode
			}
			if f.Changed("max-children") {
				patch.MaxChildren = &maxChildren
			}
			if f.Changed("share-pct") {
				patch.SharePct = &sharePct
			}
			patch.ClearMaxChildrenCap, patch.ClearSharePctCap = clearMax, clearShare
			if patch.Mode == nil && patch.MaxChildren == nil && patch.SharePct == nil && !clearMax && !clearShare {
				return clicore.InvalidInput("nothing to change: give --mode, --max-children, --share-pct or a --clear-*-cap flag")
			}
			profileID, err := coderConversation(cfg, args[0])
			if err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			store := coderhost.NewStore(db.DB)
			sid, err := store.OperatorMissionOf(profileID, args[0])
			if err != nil {
				return clicore.InvalidInput("%v", err)
			}
			pol, err := store.SetPolicy(coderhost.OperatorScope{}, profileID, sid, patch)
			if err != nil {
				return masterErr(err)
			}
			return printQueueJSON(cmd, pol)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "off, marked or all (all runs every worker task in a session of its own)")
	cmd.Flags().IntVar(&maxChildren, "max-children", 0, "how many of the mission's sessions may run at once; also the lead's cap")
	cmd.Flags().IntVar(&sharePct, "share-pct", 0, "percent of the lead's remaining budget the sessions may carve (at most 80); also the lead's cap")
	cmd.Flags().BoolVar(&clearMax, "clear-max-children-cap", false, "lift your cap on the lead's max_children")
	cmd.Flags().BoolVar(&clearShare, "clear-share-pct-cap", false, "lift your cap on the lead's share_pct")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newMissionChildrenListCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show the child-session policies and the tasks running in sessions of their own",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireMissionOperator(cfg, "read the mission child sessions"); err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			ov, err := coderhost.NewStore(db.DB).OverviewOf(cfg.ProfileID())
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, ov)
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// newMissionChildrenResolveChargeCmd is `coder sessions mission-children resolve-charge`: the person's end of an
// unresolved child (a deleted child whose cost nobody recorded; the host holds a part of the mission's budget for it
// and does not guess). The figure is added to the mission's meter, or the child is skipped with a reason; either
// writes the child's charge row and releases the hold.
func newMissionChildrenResolveChargeCmd(cfg *Deps) *cobra.Command {
	var usd float64
	var skip bool
	var reason string
	cmd := &cobra.Command{
		Use:   "resolve-charge CHILD_CONVERSATION",
		Short: "Say what a deleted mission child cost, or skip it, to end its unresolved state (the operator's)",
		Long: "A mission child whose conversation was deleted without its spend being recorded is unresolved: the host does not guess its cost, " +
			"keeps part of the mission's budget held for it and will not let its task be marked again. Give its cost with --usd N (added to the " +
			"mission's meter once) or --skip --reason TEXT (nothing added). `list` shows the unresolved children.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMissionOperator(cfg, "resolve the charge of a mission child"); err != nil {
				return err
			}
			f := cmd.Flags()
			if f.Changed("usd") == skip {
				return clicore.InvalidInput("give exactly one of --usd N or --skip (with --reason)")
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			added, err := coderhost.NewStore(db.DB).ResolveCharge(cfg.ProfileID(), args[0], usd, skip, reason)
			if err != nil {
				return clicore.InvalidInput("%v", err)
			}
			return printQueueJSON(cmd, map[string]any{"conversation": args[0], "addedUsd": added, "skipped": skip})
		},
	}
	cmd.Flags().Float64Var(&usd, "usd", 0, "what the child cost, in dollars (added to the mission's meter)")
	cmd.Flags().BoolVar(&skip, "skip", false, "charge nothing (needs --reason)")
	cmd.Flags().StringVar(&reason, "reason", "", "why the child is skipped, or where the figure comes from; kept in the charge row")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
