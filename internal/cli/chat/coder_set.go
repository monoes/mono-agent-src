package chat

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// `coder set`: coder mode's defaults, every flag of which is the operator's.

// operatorCoderFlags are the `coder set` flags only the operator changes:
// where a full-access chat works and the caps on how far it goes.
var operatorCoderFlags = []string{"workspace-root", "max-turns", "timeout", "budget-usd", "org-max-agents", "org-max-concurrent", "org-budget-usd", "default-access", "box"}

// operatorOrgFlags are the dynamic-org `coder set` flags only the operator
// changes, and why: what Jev (TypeSafe) is asked about the workers' briefs,
// the egress `jev enable` is the user's for, and whether their commits skip
// git hooks. With these, --send-approval and --app-access, every flag of `coder
// set` is the operator's.
var operatorOrgFlags = []struct{ name, why string }{
	{"org-router", "which decides what Jev (TypeSafe) is asked about the workers' briefs"},
	{"org-model-picker", "which decides what Jev (TypeSafe) is asked about the workers' briefs"},
	{"org-writers", "which decides whether the workers' commits skip git hooks"},
}

func newCoderSetCmd(cfg *Deps) *cobra.Command {
	var (
		root     string
		maxTurns int
		timeout  string
		budget   float64
		orgMax   int
		orgConc  int
		orgBudg  float64
		picker   string
		router   string
		writers  string
		sendAppr bool
		appAcc   string
		defAcc   string
		box      string
	)
	c := coderSettingsCmd(cfg, "set", "Change coder mode's defaults", func(cmd *cobra.Command, s *coderSettings) error {
		f := cmd.Flags()
		// Where a full-access chat works and how far it may go are the
		// operator's, as the send-approval setting is.
		for _, name := range operatorCoderFlags {
			if f.Changed(name) {
				if err := clicore.RequireOperator(cfg.RequireOperator, "change coder mode's --"+name+", which bounds a full-access chat"); err != nil {
					return err
				}
				break
			}
		}
		for _, o := range operatorOrgFlags {
			if f.Changed(o.name) {
				if err := clicore.RequireOperator(cfg.RequireOperator, "change coder mode's --"+o.name+", "+o.why); err != nil {
					return err
				}
				break
			}
		}
		if f.Changed("workspace-root") {
			abs, err := filepath.Abs(expandHome(root))
			if err != nil {
				return clicore.InvalidInput("--workspace-root: %v", err)
			}
			s.WorkspaceRoot = abs
		}
		if f.Changed("max-turns") {
			if maxTurns <= 0 {
				return clicore.InvalidInput("--max-turns must be positive")
			}
			s.MaxTurns = maxTurns
		}
		if f.Changed("timeout") {
			d, err := parseDurationFlag(timeout)
			if err != nil || d <= 0 {
				return clicore.InvalidInput("--timeout must be a positive duration (e.g. 30m, 2h)")
			}
			s.Timeout = d.String()
		}
		if f.Changed("budget-usd") {
			if budget < 0 {
				return clicore.InvalidInput("--budget-usd can't be negative (0 means no cap)")
			}
			s.BudgetUSD = budget
		}
		if f.Changed("org-max-agents") {
			if orgMax <= 0 || orgMax > 20 {
				return clicore.InvalidInput("--org-max-agents must be 1-20")
			}
			s.OrgMaxAgents = orgMax
		}
		if f.Changed("org-max-concurrent") {
			if orgConc <= 0 || orgConc > 10 {
				return clicore.InvalidInput("--org-max-concurrent must be 1-10")
			}
			s.OrgMaxConcurrent = orgConc
		}
		if f.Changed("org-budget-usd") {
			if orgBudg < 0 {
				return clicore.InvalidInput("--org-budget-usd can't be negative (0 means no cap)")
			}
			s.OrgBudgetUSD = orgBudg
		}
		if f.Changed("org-model-picker") {
			if picker != dynorg.PickerLead && picker != dynorg.PickerLeadThenJev {
				return clicore.InvalidInput("--org-model-picker must be %q or %q", dynorg.PickerLeadThenJev, dynorg.PickerLead)
			}
			s.OrgModelPicker = picker
		}
		if f.Changed("org-router") {
			if router != dynorg.RouterAuto && router != dynorg.RouterJev && router != dynorg.RouterModel {
				return clicore.InvalidInput("--org-router must be %q, %q or %q", dynorg.RouterAuto, dynorg.RouterJev, dynorg.RouterModel)
			}
			s.OrgRouter = router
		}
		if f.Changed("org-writers") {
			if writers != dynorg.WritersShared && writers != dynorg.WritersIsolated {
				return clicore.InvalidInput("--org-writers must be %q or %q", dynorg.WritersShared, dynorg.WritersIsolated)
			}
			s.OrgWriters = writers
		}
		if f.Changed("default-access") {
			if !ai.ValidAccess(defAcc) {
				return clicore.InvalidInput("--default-access must be scoped, coding, workspace or full")
			}
			s.DefaultAccess = defAcc
		}
		if f.Changed("box") {
			if box != BoxOff && box != BoxCompat && box != BoxStrict {
				return clicore.InvalidInput("--box must be %s, %s or %s", BoxOff, BoxCompat, BoxStrict)
			}
			s.Box = box
		}
		if f.Changed("send-approval") {
			if err := clicore.RequireOperator(cfg.RequireOperator, "change the send-approval setting"); err != nil {
				return err
			}
			s.SendApproval = &sendAppr
		}
		if f.Changed("app-access") {
			if err := clicore.RequireOperator(cfg.RequireOperator, "change the app-access setting"); err != nil {
				return err
			}
			if appAcc != "on" && appAcc != "off" {
				return clicore.InvalidInput("--app-access must be on or off")
			}
			on := appAcc == "on"
			s.AppAccess = &on
		}
		return nil
	})
	c.Long = "Change coder mode's defaults. " + clicore.OperatorOnlyHelp +
		" Without flags it only prints the settings, which anyone may, and `coder disable` is anyone's too: it only turns coder mode off.\n\n" +
		"--app-access changes what agents are told and offered, not what they can do: off, coder prompts carry no profile summary " +
		"and a dynamic org's workers are not offered MonoAgent's surfaces; the coder lead keeps full access, a worker with a shell " +
		"can still run monoagentcli, and operator-only commands and the send gate apply either way. With a Jev key set, a worker's " +
		"brief goes to Jev (TypeSafe) whatever app access and the router say: auto and jev route with it, and Jev picks the model " +
		"and effort the lead leaves open, with --org-router model too. Only removing the Jev key keeps briefs on this machine."
	c.Flags().BoolVar(&sendAppr, "send-approval", true, "An agent's sends (email, DM, comment, post, connect, publish) wait for your approval (operator only)")
	c.Flags().StringVar(&appAcc, "app-access", "", "on (default) or off: what agents are told and offered, not what they can do. Off, coder prompts carry no profile summary and a dynamic org's workers are not offered the workspace profile (MonoAgent's tasks, documents, people, vault, workflows and apps) or told about those surfaces; the coder lead keeps full access, a worker with a shell can still run monoagentcli, operator-only commands and the send gate still apply, and with a Jev key set a worker's brief still goes to Jev, whatever the router (operator only)")
	c.Flags().StringVar(&defAcc, "default-access", "", "scoped, coding, workspace or full (default full): the level an operator-run chat creation without --access makes when coder mode is ready; an agent-run creation stays scoped (operator only)")
	c.Flags().StringVar(&box, "box", "", "off (default), compat or strict: run a coding-access coder turn inside the agent sandbox (Linux, bubblewrap). Workspace and full access turns need the user's database, which the sandbox hides, so they are refused while this is on (operator only)")
	c.Flags().StringVar(&root, "workspace-root", "", "Folder new test workspaces are created in (operator only)")
	c.Flags().IntVar(&maxTurns, "max-turns", 0, "Agent turn cap per message (operator only)")
	c.Flags().StringVar(&timeout, "timeout", "", "Wall-clock cap per message (e.g. 60m; operator only)")
	c.Flags().Float64Var(&budget, "budget-usd", 0, "Spend cap per message in USD (0 = none; operator only)")
	c.Flags().IntVar(&orgMax, "org-max-agents", 0, "Dynamic org: workers the lead may spawn per message (default 6; operator only)")
	c.Flags().IntVar(&orgConc, "org-max-concurrent", 0, "Dynamic org: workers running at once (default 3; operator only)")
	c.Flags().Float64Var(&orgBudg, "org-budget-usd", 0, "Dynamic org: worker cost cap per message in USD, estimated for runtimes that report none (0 = none; operator only)")
	c.Flags().StringVar(&picker, "org-model-picker", "", "Dynamic org: who picks a worker's model when the lead doesn't: lead-then-jev (default) or lead (operator only)")
	c.Flags().StringVar(&router, "org-router", "", "Dynamic org (coder.router): who picks a worker's role and skills when the lead doesn't: auto (default; monomind pick when confident, else Jev, else the lead), jev or model (the lead names roles from org_roster; operator only)")
	c.Flags().StringVar(&writers, "org-writers", "", "Dynamic org: shared (default; one writer at a time under the write lease) or isolated (each writer gets its own git worktree and branch, merged by the lead; its checkpoint commits skip git hooks, so merged work never passed pre-commit, and the worktrees under .monoagent-worktrees/ are visible to tools that ignore git excludes; operator only)")
	return c
}
