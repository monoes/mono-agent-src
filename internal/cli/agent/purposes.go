package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentpurposes"
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// newAgentPurposesCmd is `agent purposes`: which models each kind of agent
// work may use (internal/agentpurposes). list is open to anyone; set and
// reset are the user's own.
func (d Deps) newAgentPurposesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purposes",
		Short: "Choose which validated models each kind of agent work may use",
		Long: "Each purpose (coding, review, research, ...) can be limited to the validated runtime and model pairs " +
			"you choose; an agent that starts sub-agents (the dynamic org) picks their models from that set only. " +
			"A purpose with nothing chosen uses every validated model. " +
			"list shows the purposes and the roster with each pair's status; set and reset change them and are the user's own.",
	}
	cmd.AddCommand(d.newAgentPurposesListCmd(), d.newAgentPurposesSetCmd(), d.newAgentPurposesResetCmd())
	return cmd
}

// purposeView is one purpose in `purposes list --json`.
type purposeView struct {
	ID                  string                     `json:"id"`
	Title               string                     `json:"title"`
	Category            string                     `json:"category"`
	Description         string                     `json:"description"`
	Rule                string                     `json:"rule"`
	Pairs               []agentpurposes.RosterPair `json:"pairs"`
	Effort              string                     `json:"effort"`
	DifferentFromAuthor bool                       `json:"differentFromAuthor"`
	Fallback            string                     `json:"fallback"`
	BudgetSharePct      int                        `json:"budgetSharePct"`
	IsDefault           bool                       `json:"isDefault"`
}

// purposesView is `purposes list --json`.
type purposesView struct {
	Purposes []purposeView              `json:"purposes"`
	Roster   []agentpurposes.RosterPair `json:"roster"`
}

func viewOf(p agentpurposes.Purpose, set agentpurposes.Setting, roster []agentpurposes.RosterPair) purposeView {
	pairs := make([]agentpurposes.RosterPair, len(set.Pairs))
	for i, pr := range set.Pairs {
		pairs[i] = agentpurposes.RosterPair{Runtime: pr.Runtime, Model: pr.Model, Status: agentpurposes.StatusIn(roster, pr)}
	}
	return purposeView{
		ID: p.ID, Title: p.Title, Category: p.Category, Description: p.Description,
		Rule: set.Rule, Pairs: pairs, Effort: set.Effort, DifferentFromAuthor: set.DifferentFromAuthor,
		Fallback: set.Fallback, BudgetSharePct: set.BudgetSharePct, IsDefault: set.IsDefault(),
	}
}

// loadRoster is the validated roster as pairs with their status, from the
// stored results alone: no scan runs, so a command that only reads or
// changes settings starts nothing. A runtime that is no longer installed
// still reads ok here; the spawn path checks installs (dynorg).
func loadRoster(results []agentroster.Result) []agentpurposes.RosterPair {
	return agentpurposes.FromRoster(agentroster.Build(results, nil, time.Now(), agentroster.DefaultMaxAge))
}

func (d Deps) newAgentPurposesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the purposes, the models chosen for each, and the validated roster",
		Long: "Lists every purpose with the pairs chosen for it, each with its roster status (ok, stale, failed or unvalidated), " +
			"and the roster to choose from. A purpose with isDefault true has nothing chosen and uses every validated pair. " +
			"A stored setting that can't be read counts as the default and is reported on stderr.",
		Example: `  monoagentcli --profile work agent purposes list --json`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			results, err := agentroster.List(cmd.Context(), db.DB)
			if err != nil {
				return err
			}
			roster := loadRoster(results)
			store := agentpurposes.Store{DB: db, ProfileID: d.ProfileID()}
			all, err := store.LoadAll()
			if err != nil {
				return err
			}
			out := purposesView{Purposes: []purposeView{}, Roster: roster}
			for _, p := range agentpurposes.Builtins() {
				l := all[p.ID]
				if l.Err != nil {
					fmt.Fprintf(os.Stderr, "warning: the stored setting for %s is %v; it counts as the default (reset it with: monoagentcli agent purposes reset %s)\n", p.ID, l.Err, p.ID)
				}
				out.Purposes = append(out.Purposes, viewOf(p, l.Setting, roster))
			}
			if d.JSONOutput() {
				return printJSON(out)
			}
			table := d.NewTable(os.Stdout, []string{"Purpose", "Models", "Rule", "Different from author", "Fallback"}, nil)
			for _, p := range out.Purposes {
				models := "all validated"
				if !p.IsDefault {
					parts := make([]string, len(p.Pairs))
					for i, pr := range p.Pairs {
						parts[i] = pr.Runtime + ":" + pr.Model
						if pr.Status != agentpurposes.StatusOK {
							parts[i] += " (" + pr.Status + ")"
						}
					}
					models = strings.Join(parts, ", ")
				}
				diff := "off"
				if p.DifferentFromAuthor {
					diff = "on"
				}
				table.Append([]string{p.ID, models, p.Rule, diff, p.Fallback})
			}
			table.Render()
			return nil
		},
	}
}

// refuseUnlessOperator is the user's-own check of set and reset, made before
// anything is read.
func (d Deps) refuseUnlessOperator(what string) error {
	if why := clicore.OperatorRefusal(d.Refusal); why != "" {
		return clicore.OperatorOnly("%s changes which models agents may run on, which only the user does: %s; run it yourself in your terminal or the app", what, why)
	}
	return nil
}

// purposeArg is the one PURPOSE argument, checked after the operator gate.
func (d Deps) purposeArg(args []string) (agentpurposes.Purpose, error) {
	if len(args) != 1 {
		return agentpurposes.Purpose{}, d.InvalidInput("give exactly one purpose: %s", strings.Join(agentpurposes.IDs(), ", "))
	}
	p, ok := agentpurposes.Lookup(args[0])
	if !ok {
		return p, d.InvalidInput("unknown purpose %q; use one of: %s", args[0], strings.Join(agentpurposes.IDs(), ", "))
	}
	return p, nil
}

func (d Deps) newAgentPurposesSetCmd() *cobra.Command {
	var (
		pairs                  []string
		rule, effort, diff, fb string
		share                  int
	)
	cmd := &cobra.Command{
		Use:   "set PURPOSE",
		Short: "Choose the validated models a purpose may use (replaces its whole setting)",
		Long: "Replaces the setting of one purpose with exactly what the flags say; a flag left out takes its default. " +
			"Each --pair RUNTIME:MODEL must be a validated pair of the roster (status ok); one that is not is refused. " +
			"--rule: any (the usual ranking of fit, track record and cost inside the set), order (the first available pair in the order given) " +
			"or cheapest (the cheapest that meets the quality bar). " +
			"--different-from-author on makes a worker of this purpose run a different model than the one that produced the work it checks; " +
			"with only one validated model it uses that one and says so. " +
			"--fallback none (default) fails a spawn when no chosen pair is available; any lets it use every validated model. " +
			"--budget-share is recorded as a percentage; the spend caps do not read it yet. " +
			clicore.OperatorOnlyHelp,
		Example: `  monoagentcli --profile work agent purposes set review --pair claude:opus --pair codex:gpt-5.5 --different-from-author on --json
  monoagentcli --profile work agent purposes set triage --pair claude:haiku --rule cheapest --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.refuseUnlessOperator("agent purposes set"); err != nil {
				return err
			}
			p, err := d.purposeArg(args)
			if err != nil {
				return err
			}
			set, err := d.settingFromFlags(pairs, rule, effort, diff, fb, share)
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			results, err := agentroster.List(cmd.Context(), db.DB)
			if err != nil {
				return err
			}
			roster := loadRoster(results)
			if err := agentpurposes.CheckValidated(set, roster); err != nil {
				return d.InvalidInput("%v", err)
			}
			if err := (agentpurposes.Store{DB: db, ProfileID: d.ProfileID()}).Save(p.ID, set); err != nil {
				return err
			}
			return d.printPurpose(p, set, roster, "Set")
		},
	}
	cmd.Flags().StringArrayVar(&pairs, "pair", nil, "A validated RUNTIME:MODEL the purpose may use (repeatable; the order matters for --rule order)")
	cmd.Flags().StringVar(&rule, "rule", agentpurposes.RuleAny, "How to choose inside the set: any, order or cheapest")
	cmd.Flags().StringVar(&effort, "effort", "", "Reasoning effort to ask for when the model offers it")
	cmd.Flags().StringVar(&diff, "different-from-author", "off", "on: run a different model than the one that produced the work (on or off)")
	cmd.Flags().StringVar(&fb, "fallback", agentpurposes.FallbackNone, "When no chosen pair is available: none fails the spawn, any uses every validated model")
	cmd.Flags().IntVar(&share, "budget-share", 0, "Share of the budget in percent, 0 to 100 (recorded only)")
	return cmd
}

// settingFromFlags builds the setting a set command asks for.
func (d Deps) settingFromFlags(pairs []string, rule, effort, diff, fallback string, share int) (agentpurposes.Setting, error) {
	set := agentpurposes.Setting{Pairs: []agentpurposes.Pair{}, Rule: rule, Effort: effort, Fallback: fallback, BudgetSharePct: share}
	switch diff {
	case "on":
		set.DifferentFromAuthor = true
	case "off":
	default:
		return set, d.InvalidInput("--different-from-author is on or off, not %q", diff)
	}
	for _, raw := range pairs {
		runtime, model, ok := strings.Cut(raw, ":")
		if !ok || runtime == "" || model == "" {
			return set, d.InvalidInput("--pair %q: write it as RUNTIME:MODEL, for example claude:opus", raw)
		}
		set.Pairs = append(set.Pairs, agentpurposes.Pair{Runtime: runtime, Model: model})
	}
	if err := set.Validate(); err != nil {
		return set, d.InvalidInput("%v", err)
	}
	return set, nil
}

func (d Deps) newAgentPurposesResetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "reset PURPOSE",
		Short:   "Put a purpose back to the default: every validated model",
		Long:    "Clears what was chosen for one purpose, so it uses every validated model again. " + clicore.OperatorOnlyHelp,
		Example: `  monoagentcli --profile work agent purposes reset review --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.refuseUnlessOperator("agent purposes reset"); err != nil {
				return err
			}
			p, err := d.purposeArg(args)
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			if err := (agentpurposes.Store{DB: db, ProfileID: d.ProfileID()}).Reset(p.ID); err != nil {
				return err
			}
			return d.printPurpose(p, agentpurposes.Default(), nil, "Reset")
		},
	}
	return cmd
}

// printPurpose reports the purpose a set or reset left: the same object a
// list holds for it.
func (d Deps) printPurpose(p agentpurposes.Purpose, set agentpurposes.Setting, roster []agentpurposes.RosterPair, verb string) error {
	v := viewOf(p, set, roster)
	if d.JSONOutput() {
		return printJSON(v)
	}
	if v.IsDefault {
		fmt.Printf("%s %s: every validated model\n", verb, p.ID)
		return nil
	}
	parts := make([]string, len(v.Pairs))
	for i, pr := range v.Pairs {
		parts[i] = pr.Runtime + ":" + pr.Model
	}
	fmt.Printf("%s %s: %s (rule %s)\n", verb, p.ID, strings.Join(parts, ", "), v.Rule)
	return nil
}
