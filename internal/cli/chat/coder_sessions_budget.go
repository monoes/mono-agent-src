package chat

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions budget`: the person's answer to a mission lead's request for more budget (phase P-F of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md). The lead files a request with its own tool
// (mission_budget_request) and it waits here for as long as it takes. grant and decline are the only answer
// paths and both are the operator's: a process that holds a coder session token or a mission binding is refused
// before anything is read, so no agent can answer, reopen or time out a request, its own or another's.

func newCoderSessionsBudgetCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "Answer a mission lead's request for more budget (the operator's)",
		Long: "A mission lead whose budget is used up may ask for more; the request waits for you with no timeout and changes " +
			"nothing until you answer. `grant` adds the amount (at most what was asked) to the one session the request names, " +
			"once, and only if the session it is carved from can still cover it; `decline` closes it. `list` shows the requests " +
			"with the numbers the host read, never the lead's own words as an instruction.",
	}
	cmd.AddCommand(newBudgetListCmd(cfg), newBudgetGrantCmd(cfg), newBudgetDeclineCmd(cfg))
	return cmd
}

func newBudgetListCmd(cfg *Deps) *cobra.Command {
	var open bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show the budget requests (--open: only those waiting for you now)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireMissionOperator(cfg, "read the budget requests"); err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			out, err := coderhost.NewStore(db.DB).ListBudgetRequests(cfg.ProfileID(), open)
			if err != nil {
				return masterErr(err)
			}
			return printQueueJSON(cmd, out)
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "only the requests waiting for you now")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newBudgetGrantCmd(cfg *Deps) *cobra.Command {
	var usd float64
	cmd := &cobra.Command{
		Use:   "grant REQUEST_ID",
		Short: "Give the session a request names the budget it asked for (or less, with --usd)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMissionOperator(cfg, "grant a budget request"); err != nil {
				return err
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return clicore.InvalidInput("the request id is a number from `coder sessions budget list`")
			}
			if cmd.Flags().Changed("usd") && !(usd > 0) {
				return clicore.InvalidInput("--usd must be above 0")
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			b, err := coderhost.NewStore(db.DB).GrantBudget(coderhost.OperatorScope{}, cfg.ProfileID(), id, usd)
			if err != nil {
				return masterErr(err)
			}
			return printQueueJSON(cmd, b)
		},
	}
	cmd.Flags().Float64Var(&usd, "usd", 0, "grant this much (at most what was asked); default: the amount asked")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newBudgetDeclineCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decline REQUEST_ID",
		Short: "Close a budget request without giving anything",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMissionOperator(cfg, "decline a budget request"); err != nil {
				return err
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return clicore.InvalidInput("the request id is a number from `coder sessions budget list`")
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			b, err := coderhost.NewStore(db.DB).DeclineBudget(coderhost.OperatorScope{}, cfg.ProfileID(), id)
			if err != nil {
				return masterErr(err)
			}
			return printQueueJSON(cmd, b)
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
