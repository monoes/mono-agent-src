package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentpurposes"
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/monomind"
)

// `agent purposes`: what the operator chose for each kind of work agents do (internal/agentpurposes). The
// list is open to anyone; set and reset are the operator's, checked before anything is read.

// pairView is one (runtime, model) as the pane shows it, with the roster's state for it.
type pairView struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"` // "default" is the runtime's default model
	Label   string `json:"label,omitempty"`
	// State is the roster's: ready, stale, failed or untested; unknown for a chosen pair the roster no longer has.
	State string `json:"state"`
	// Selectable: the pair can be chosen and used (ready or stale).
	Selectable bool `json:"selectable"`
}

type purposeView struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	Categories          []string   `json:"categories"`
	Configured          bool       `json:"configured"`
	Pairs               []pairView `json:"pairs"`
	Rule                string     `json:"rule"`
	Effort              string     `json:"effort"`
	DifferentFromAuthor bool       `json:"different_from_author"`
	BudgetSharePct      int        `json:"budget_share_pct"`
	Fallback            bool       `json:"fallback"`
}

type purposesListView struct {
	Purposes  []purposeView `json:"purposes"`
	Available []pairView    `json:"available"`
}

const unknownState = "unknown"

func displayModel(m string) string {
	if m == "" {
		return agentroster.DefaultModel
	}
	return m
}

func selectableState(state string) bool {
	return state == agentroster.StateReady || state == agentroster.StateStale
}

// rosterPairs reads the validated roster: every pair with its state, the model's effort levels by pair key.
func (d Deps) rosterPairs(ctx context.Context, noScan bool, results []agentroster.Result) ([]pairView, map[string][]string) {
	var scan *monomind.ScanResult
	if !noScan {
		if s, err := monomind.Scan(ctx); err == nil {
			scan = s
		}
	}
	var out []pairView
	efforts := map[string][]string{}
	for _, rr := range agentroster.Build(results, scan, time.Now(), agentroster.DefaultMaxAge) {
		for _, m := range rr.Models {
			p := pairView{Runtime: rr.Runtime, Model: displayModel(m.Model), Label: m.Label, State: m.State, Selectable: selectableState(m.State)}
			out = append(out, p)
			efforts[rr.Runtime+"/"+displayModel(m.Model)] = m.EffortLevels
		}
	}
	return out, efforts
}

func viewOf(info agentpurposes.Info, s agentpurposes.Settings, configured bool, states map[string]pairView) purposeView {
	v := purposeView{
		ID: info.ID, Title: info.Title, Description: info.Description, Categories: append([]string{}, info.Categories...),
		Configured: configured, Pairs: []pairView{}, Rule: s.EffectiveRule(), Effort: s.Effort,
		DifferentFromAuthor: s.DifferentFromAuthor, BudgetSharePct: s.BudgetSharePct, Fallback: s.Fallback,
	}
	for _, p := range s.Pairs {
		pv, ok := states[p.Runtime+"/"+displayModel(p.Model)]
		if !ok {
			pv = pairView{Runtime: p.Runtime, Model: displayModel(p.Model), State: unknownState}
		}
		v.Pairs = append(v.Pairs, pv)
	}
	return v
}

func (d Deps) newAgentPurposesCmd() *cobra.Command {
	var noScan bool
	cmd := &cobra.Command{
		Use:   "purposes",
		Short: "Which models a parent may pick for each kind of work (list; set and reset are yours only)",
		Long: "A purpose is a kind of work agents do (coding, reviewing, researching, …). For each you can choose the\n" +
			"(runtime, model) pairs a parent may pick from when it starts an agent for it, and how: any of them (the code\n" +
			"ranks them by fit, track record and cost), in your order, or the cheapest that meets the quality bar. A parent\n" +
			"cannot name a pair outside the set and cannot edit it: the choice is enforced where the agent is started.\n" +
			"Only validated pairs (ready or stale on the roster) can be chosen. A purpose with nothing chosen uses every\n" +
			"validated pair, as before. " + clicore.OperatorOnlyHelp,
	}
	cmd.PersistentFlags().BoolVar(&noScan, "no-scan", false, "Skip the runtime scan (pair states are then computed from age only)")
	cmd.AddCommand(d.newPurposesListCmd(&noScan), d.newPurposesSetCmd(&noScan), d.newPurposesResetCmd())
	return cmd
}

// load reads the purposes and the roster once.
func (d Deps) loadPurposes(ctx context.Context, noScan bool) (agentpurposes.Set, []pairView, map[string][]string, error) {
	db, err := d.OpenDB()
	if err != nil {
		return nil, nil, nil, err
	}
	defer db.Close()
	set, err := agentpurposes.Load(ctx, db.DB)
	if err != nil {
		return nil, nil, nil, err
	}
	results, err := agentroster.List(ctx, db.DB)
	if err != nil {
		return nil, nil, nil, err
	}
	pairs, efforts := d.rosterPairs(ctx, noScan, results)
	return set, pairs, efforts, nil
}

func stateIndex(pairs []pairView) map[string]pairView {
	m := map[string]pairView{}
	for _, p := range pairs {
		m[p.Runtime+"/"+p.Model] = p
	}
	return m
}

func (d Deps) newPurposesListCmd(noScan *bool) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List the purposes with the models chosen for each and the validated roster",
		Example: "  monoagentcli agent purposes list --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, pairs, _, err := d.loadPurposes(cmd.Context(), *noScan)
			if err != nil {
				return err
			}
			states := stateIndex(pairs)
			view := purposesListView{Purposes: []purposeView{}, Available: pairs}
			if view.Available == nil {
				view.Available = []pairView{}
			}
			for _, info := range agentpurposes.Catalog {
				s, configured := set[info.ID]
				view.Purposes = append(view.Purposes, viewOf(info, s, configured && s.Restricts(), states))
			}
			if d.JSONOutput() {
				return printJSON(view)
			}
			table := d.NewTable(os.Stdout, []string{"Purpose", "Chosen", "Rule", "Notes"}, nil)
			for _, p := range view.Purposes {
				chosen := "all validated"
				if len(p.Pairs) > 0 {
					var parts []string
					for _, pr := range p.Pairs {
						parts = append(parts, fmt.Sprintf("%s/%s (%s)", pr.Runtime, pr.Model, pr.State))
					}
					chosen = strings.Join(parts, ", ")
				}
				var notes []string
				if p.DifferentFromAuthor {
					notes = append(notes, "different model than the author")
				}
				if p.Effort != "" {
					notes = append(notes, "effort "+p.Effort)
				}
				if p.Fallback {
					notes = append(notes, "may fall back")
				}
				table.Append([]string{p.ID, chosen, p.Rule, strings.Join(notes, "; ")})
			}
			return table.Render()
		},
	}
}

func (d Deps) newPurposesSetCmd(noScan *bool) *cobra.Command {
	var pairFlags []string
	var rule, effort string
	var differentModel, fallback bool
	var share int
	cmd := &cobra.Command{
		Use:   "set PURPOSE [--pair RUNTIME/MODEL]... [--rule any|order|cheapest] [--effort LEVEL] [--different-model] [--budget-share N] [--fallback]",
		Short: "Choose the models for a purpose (replaces what was set; you only)",
		Long: "Replaces the settings of PURPOSE with the flags given (flags not given are the defaults), so a settings pane sends\n" +
			"its whole state each time. Each --pair is runtime/model (a runtime alone, or runtime/default, is its default model)\n" +
			"and must be validated: ready or stale on `agent roster`. With no --pair nothing is chosen and every validated pair is\n" +
			"used. --rule: any (default), order (the first available in the order given) or cheapest. --effort must be a level one\n" +
			"of the chosen models offers. --different-model: never the model that wrote the work (reviewing, verifying, judging).\n" +
			"--fallback: if none of the chosen pairs is available, a spawn may use any validated pair. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli agent purposes set reviewing --pair claude/opus --pair codex/gpt-5 --rule order --different-model",
		Args:    cobra.ArbitraryArgs, // counted below, after the operator check
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(d.RequireOperator, "change which models agents may use for a purpose"); err != nil {
				return err
			}
			if len(args) != 1 {
				return d.InvalidInput("agent purposes set takes one purpose (got %d arguments)", len(args))
			}
			info, ok := agentpurposes.Find(args[0])
			if !ok {
				return d.NotFound("%q is not a purpose; see `agent purposes list`", args[0])
			}
			var s agentpurposes.Settings
			for _, raw := range pairFlags {
				p, err := agentpurposes.ParsePair(raw)
				if err != nil {
					return d.InvalidInput("%v", err)
				}
				s.Pairs = append(s.Pairs, p)
			}
			s.Rule, s.Effort, s.DifferentFromAuthor, s.BudgetSharePct, s.Fallback = rule, effort, differentModel, share, fallback
			s = s.Normalize()
			if err := s.Validate(); err != nil {
				return d.InvalidInput("%v", err)
			}
			ctx := cmd.Context()
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			set, err := agentpurposes.Load(ctx, db.DB)
			if err != nil {
				return err
			}
			results, err := agentroster.List(ctx, db.DB)
			if err != nil {
				return err
			}
			pairs, efforts := d.rosterPairs(ctx, *noScan, results)
			states := stateIndex(pairs)
			offered := map[string]bool{}
			for _, p := range s.Pairs {
				key := p.Runtime + "/" + displayModel(p.Model)
				pv, known := states[key]
				if !known || !pv.Selectable {
					state := unknownState
					if known {
						state = pv.State
					}
					return d.InvalidInput("%s cannot be chosen: it is %s on the roster (only ready or stale pairs can be; see `agent roster`)", key, state)
				}
				for _, l := range efforts[key] {
					offered[l] = true
				}
			}
			if s.Effort != "" && len(s.Pairs) > 0 && !offered[s.Effort] {
				return d.InvalidInput("effort %q is not a level of the chosen models", s.Effort)
			}
			if s.IsZero() {
				delete(set, info.ID)
			} else {
				set[info.ID] = s
			}
			if err := agentpurposes.Save(ctx, db.DB, set); err != nil {
				return err
			}
			cur, configured := set[info.ID]
			view := viewOf(info, cur, configured && cur.Restricts(), states)
			if d.JSONOutput() {
				return printJSON(map[string]any{"purpose": view})
			}
			fmt.Printf("Set %s: %d model(s) chosen, rule %s\n", info.ID, len(view.Pairs), view.Rule)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&pairFlags, "pair", nil, "A runtime/model the purpose may use; repeat for several")
	cmd.Flags().StringVar(&rule, "rule", "", "How the pairs are used: any (default), order or cheapest")
	cmd.Flags().StringVar(&effort, "effort", "", "A reasoning effort level to use when the chosen model offers it")
	cmd.Flags().BoolVar(&differentModel, "different-model", false, "Never the model that wrote the work (reviewing, verifying, judging)")
	cmd.Flags().IntVar(&share, "budget-share", 0, "Share of the budget this purpose may use, in percent (stored; not enforced yet)")
	cmd.Flags().BoolVar(&fallback, "fallback", false, "If no chosen pair is available, allow any validated pair")
	return cmd
}

func (d Deps) newPurposesResetCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "reset (PURPOSE | --all)",
		Short:   "Forget what was chosen for a purpose, or for all (you only)",
		Long:    "Back to the default: the purpose uses every validated pair. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli agent purposes reset reviewing\n  monoagentcli agent purposes reset --all",
		Args:    cobra.ArbitraryArgs, // counted below, after the operator check
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(d.RequireOperator, "change which models agents may use for a purpose"); err != nil {
				return err
			}
			if (all && len(args) != 0) || (!all && len(args) != 1) {
				return d.InvalidInput("give one purpose, or --all (not both)")
			}
			if !all {
				if _, ok := agentpurposes.Find(args[0]); !ok {
					return d.NotFound("%q is not a purpose; see `agent purposes list`", args[0])
				}
			}
			ctx := cmd.Context()
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			set, err := agentpurposes.Load(ctx, db.DB)
			if err != nil {
				return err
			}
			reset := []string{}
			for _, info := range agentpurposes.Catalog {
				if _, has := set[info.ID]; has && (all || info.ID == args[0]) {
					reset = append(reset, info.ID)
					delete(set, info.ID)
				}
			}
			if len(reset) > 0 {
				if err := agentpurposes.Save(ctx, db.DB, set); err != nil {
					return err
				}
			}
			if d.JSONOutput() {
				return printJSON(map[string]any{"reset": reset})
			}
			fmt.Printf("Reset %d purpose(s)\n", len(reset))
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Reset every purpose")
	return cmd
}
