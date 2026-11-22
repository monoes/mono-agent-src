package org

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/storage"
)

func newOrgAutonomyDecisionsCmd(env *orgEnv) *cobra.Command {
	var run, verdict string
	var limit int
	c := &cobra.Command{
		Use:   "decisions <org>",
		Short: "List the decisions made for an org (resolver, tier, verdict, rationale, cost)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, _, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			ds, err := orgdecide.NewStore(db.DB).List(cmd.Context(), profileID, doc.Name, orgdecide.DecisionFilter{RunID: run, Verdict: verdict, Limit: limit})
			if err != nil {
				return err
			}
			return printJSONValue(map[string]interface{}{"v": 1, "org": doc.Name, "decisions": decisionRows(ds)})
		},
	}
	c.Flags().StringVar(&run, "run", "", "Only this org run")
	c.Flags().StringVar(&verdict, "verdict", "", "approved | denied | answered | escalated | failed")
	c.Flags().IntVar(&limit, "limit", 200, "Most recent N")
	return c
}

// decisionRow is one `org autonomy decisions` row: the stored decision plus,
// when TypeSafe Jev was asked, a summary of its answer.
type decisionRow struct {
	orgdecide.Decision
	Jev *jevDecisionView `json:"jev,omitempty"`
}

// jevDecisionView summarises a jev decider's answer on a decision row.
// Decided is false when Jev's top verdict fell below the threshold (or Jev
// failed) and the fallback model decided; Threshold is the gate in force for
// that decision when the row records it, else null.
type jevDecisionView struct {
	Decided   bool     `json:"decided"`
	Verdict   string   `json:"verdict,omitempty"`
	P         float64  `json:"p"`
	Threshold *float64 `json:"threshold"`
	// Provider is the decision provider that answered when it was not
	// TypeSafe (whose rows name the model alone).
	Provider string `json:"provider,omitempty"`
}

// jevBelowPattern reads the gate from a fallback rationale
// ("jev <label>: <verdict> p=0.62 below 0.80 (...)", the label being
// "<model>" for TypeSafe or "<provider>/<model>").
var jevBelowPattern = regexp.MustCompile(`^jev \S+: \S+ p=[0-9.]+ below ([0-9.]+)`)

// jevProviderPattern reads a provider from a resolver ("jev:<provider>/<model>")
// or a rationale ("jev <provider>/<model>: …", "jev <provider>/<model> failed …").
// A provider name has no "/" and TypeSafe models have none either, so the
// first "/" ends the provider; a TypeSafe label ("jev:jev-1.13") has none.
var jevProviderPattern = regexp.MustCompile(`^jev[: ]([a-z0-9-]{1,32})/\S`)

// jevProvider is the provider named by a decision's resolver or, when the
// fallback model decided, its rationale; "" for TypeSafe.
func jevProvider(d orgdecide.Decision) string {
	for _, s := range []string{d.Resolver, d.Rationale} {
		if m := jevProviderPattern.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	return ""
}

func decisionRows(ds []orgdecide.Decision) []decisionRow {
	rows := make([]decisionRow, 0, len(ds))
	for _, d := range ds {
		rows = append(rows, decisionRow{Decision: d, Jev: jevView(d)})
	}
	return rows
}

func jevView(d orgdecide.Decision) *jevDecisionView {
	decided := strings.HasPrefix(d.Resolver, "jev:")
	if !decided && len(d.Probabilities) == 0 {
		return nil
	}
	v := &jevDecisionView{Decided: decided, Provider: jevProvider(d)}
	keys := make([]string, 0, len(d.Probabilities))
	for k := range d.Probabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys) // ties go to the first verdict by name
	for _, k := range keys {
		if p := d.Probabilities[k]; v.Verdict == "" || p > v.P {
			v.Verdict, v.P = k, p
		}
	}
	if m := jevBelowPattern.FindStringSubmatch(d.Rationale); m != nil {
		if t, err := strconv.ParseFloat(m[1], 64); err == nil {
			v.Threshold = &t
		}
	}
	return v
}

func newOrgAutonomyNeedsYouCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "needs-you <org>",
		Short: "List pending items waiting for a person",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, root, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			items, err := env.needsYou(cmd.Context(), db, profileID, root, doc.Name)
			if err != nil {
				return err
			}
			return printJSONValue(map[string]interface{}{"v": 1, "org": doc.Name, "items": items})
		},
	}
}

// needsYou lists pending items a person has to decide: everything when no
// daemon runs; otherwise items the level routes to a person, and items the
// daemon escalated or could not decide.
func (env *orgEnv) needsYou(ctx context.Context, db *storage.Database, profileID, root, org string) ([]map[string]interface{}, error) {
	svc := orgdecide.NewService(db.DB, nil)
	wfs := orgenv.GrantWorkflows(db, env.OpenWorkflowStore)
	svc.LoadWorkflow, svc.ListWorkflows = wfs.GetWorkflow, wfs.ListWorkflows
	a, err := svc.Store.Get(ctx, profileID, org)
	if err != nil {
		return nil, err
	}
	pending, _, _, err := svc.Pending(ctx, profileID, root, org, a)
	if err != nil {
		return nil, err
	}
	_, daemonLive := daemonhb.Read()
	level := a.EffectiveLevel(time.Now())
	idleSeconds, idleHold := idleDeadline(ctx, root, org)
	out := []map[string]interface{}{}
	for _, it := range pending {
		human := !daemonLive || orgdecide.RouteItem(level, it) == orgdecide.RouteHuman
		if !human {
			latest, err := svc.Store.LatestFor(ctx, profileID, org, it.Kind, it.Ref)
			if err != nil {
				return nil, err
			}
			human = latest != nil && latest.Level == level &&
				(latest.Verdict == orgdecide.VerdictEscalated || (latest.Verdict == orgdecide.VerdictFailed && (level != orgdesign.LevelFull || a.OnDeciderFailure == "human")))
		}
		if !human {
			continue
		}
		waiting := ""
		if it.WaitingMS > 0 {
			waiting = time.UnixMilli(it.WaitingMS).UTC().Format(time.RFC3339)
		}
		out = append(out, map[string]interface{}{
			"kind": it.Kind, "ref": it.Ref, "requester": it.Requester, "class": it.Class, "tier": it.Tier,
			"summary": it.Summary, "waiting_since": waiting,
			"idle_stop_in_seconds": idleSeconds, "idle_hold": idleHold,
		})
	}
	return out, nil
}
