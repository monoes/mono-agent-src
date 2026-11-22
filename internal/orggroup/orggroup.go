// Package orggroup implements holding orgs (plan §7.5, U7–U11): an org whose
// root role — the Initiator — starts, stops, and hears from child orgs.
// Children stay ordinary orgs. This package holds what mono-agent adds on
// top of monomind: the budget roll-up, report-up instructions, and group
// status.
package orggroup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
)

// GroupBudgetKey is the holding org's run_config key for the group's USD
// ceiling, spent per run: both halves of the roll-up — monomind's `org
// costs` and the decider spend in org_decisions — are scoped to each org's
// current run, so a finished run's spend never refuses a fresh one.
// budget_share on each child is a fraction of it. It is never passed to
// monomind's `org run --budget-usd`, an upfront estimate gate that does
// not bound spend (C-44).
const GroupBudgetKey = "group_budget_usd"

// Initiator tool names.
const (
	ToolOrgStart  = "org_start"
	ToolOrgStop   = "org_stop"
	ToolOrgStatus = "org_status"
	ToolOrgReport = "org_report"
)

// InitiatorTools are the org tools a holding org's Initiator gets.
var InitiatorTools = []string{ToolOrgStart, ToolOrgStop, ToolOrgStatus, ToolOrgReport}

// Costs reads one org's current-run costs (monomind.OrgCosts in production).
type Costs func(ctx context.Context, root, org string) (json.RawMessage, error)

// ConfiguredCost sums an `org costs` payload over the roles defined in doc
// only: cost tables also list cross-org senders as zero-cost pseudo-roles,
// which a group total must not count (C-48).
func ConfiguredCost(raw json.RawMessage, doc *orgdesign.Doc) float64 {
	var p struct {
		Items []struct {
			Role    string  `json:"role"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return 0
	}
	var sum float64
	for _, it := range p.Items {
		if doc != nil {
			if r, _ := doc.FindRole(it.Role); r == nil || r.IsEndpoint() {
				continue
			}
		}
		sum += it.CostUSD
	}
	return sum
}

// DeciderSpend is the decider cost recorded for one org run (U11 counts
// it). run matches org_decisions.run_id the way orgdecide.Store.Usage
// does, so rows an earlier run wrote stay out of this run's roll-up.
func DeciderSpend(ctx context.Context, db *sql.DB, profileID, org, run string) float64 {
	if db == nil {
		return 0
	}
	var v sql.NullFloat64
	_ = db.QueryRowContext(ctx,
		`SELECT SUM(cost_usd) FROM org_decisions WHERE profile_id = ? AND org_name = ? AND COALESCE(run_id,'') = ?`,
		profileID, org, run).Scan(&v)
	return v.Float64
}

// GroupBudget returns the holding org's group ceiling, or 0 when unset.
func GroupBudget(h *orgdesign.Doc) float64 {
	raw, ok := h.RunConfig[GroupBudgetKey]
	if !ok {
		return 0
	}
	var v float64
	_ = json.Unmarshal(raw, &v)
	return v
}

// ChildStatus is one child's row in group status.
type ChildStatus struct {
	Org         string   `json:"org"`
	Status      string   `json:"status"`
	Run         string   `json:"run"`
	Start       string   `json:"start"`
	BudgetShare *float64 `json:"budget_share"`
	CostUSD     float64  `json:"cost_usd"`
}

// Status is `org group status`.
type Status struct {
	V        int           `json:"v"`
	Holding  string        `json:"holding"`
	Children []ChildStatus `json:"children"`
	RollupUS float64       `json:"rollup_usd"`
	Budget   float64       `json:"group_budget_usd"`
	HolderUS float64       `json:"holding_cost_usd"`
}

// Env bundles what group operations read.
type Env struct {
	DB        *sql.DB
	ProfileID string
	Root      string
	Costs     Costs
	StatusFn  func(ctx context.Context, root, org string) (json.RawMessage, error)
}

// NewEnv returns an Env over the monomind proxies.
func NewEnv(db *sql.DB, profileID, root string) *Env {
	return &Env{DB: db, ProfileID: profileID, Root: root,
		Costs: func(ctx context.Context, root, org string) (json.RawMessage, error) {
			return monomind.OrgCosts(ctx, root, org, "")
		},
		StatusFn: func(ctx context.Context, root, org string) (json.RawMessage, error) {
			return monomind.OrgStatus(ctx, root, org)
		},
	}
}

// runOf returns an org's status and current run id, both empty when its
// status is unreadable.
func (e *Env) runOf(ctx context.Context, org string) (status, run string) {
	raw, err := e.StatusFn(ctx, e.Root, org)
	if err != nil {
		return "", ""
	}
	var s struct {
		Status string `json:"status"`
		Run    string `json:"run"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return "", ""
	}
	return s.Status, s.Run
}

// orgCost is the org's spend. A cost source that cannot be read is an error
// alongside the partial figure: callers with a budget must not treat the
// missing part as free.
func (e *Env) orgCost(ctx context.Context, org, run string) (float64, error) {
	doc, _ := orgdesign.Load(e.Root, org)
	cost := DeciderSpend(ctx, e.DB, e.ProfileID, org, run)
	raw, err := e.Costs(ctx, e.Root, org)
	if err != nil {
		return cost, fmt.Errorf("cost of %q unreadable: %w", org, err)
	}
	return cost + ConfiguredCost(raw, doc), nil
}

// LoadHolding loads a holding org, refusing an ordinary one.
func (e *Env) LoadHolding(name string) (*orgdesign.Doc, error) {
	d, err := orgdesign.Load(e.Root, name)
	if err != nil {
		return nil, err
	}
	if !d.IsHolding() {
		return nil, fmt.Errorf("org %q is not a holding org (kind %q)", name, d.Kind)
	}
	return d, nil
}

// GroupStatus reports every child's state and the cost roll-up.
func (e *Env) GroupStatus(ctx context.Context, holding string) (*Status, error) {
	h, err := e.LoadHolding(holding)
	if err != nil {
		return nil, err
	}
	st := &Status{V: 1, Holding: h.Name, Children: []ChildStatus{}, Budget: GroupBudget(h)}
	_, holdingRun := e.runOf(ctx, h.Name)
	// With a budget set, a cost that cannot be read refuses rather than
	// counting as zero; without one the partial figure is only displayed.
	var costErr error
	st.HolderUS, costErr = e.orgCost(ctx, h.Name, holdingRun)
	st.RollupUS = st.HolderUS
	for _, c := range h.ChildOrgs {
		cs := ChildStatus{Org: c.Org, Start: c.Start, BudgetShare: c.BudgetShare, Status: "unknown"}
		if cs.Start == "" {
			cs.Start = "on_demand"
		}
		if status, run := e.runOf(ctx, c.Org); status != "" || run != "" {
			cs.Status, cs.Run = status, run
		}
		var err error
		if cs.CostUSD, err = e.orgCost(ctx, c.Org, cs.Run); err != nil && costErr == nil {
			costErr = err
		}
		st.RollupUS += cs.CostUSD
		st.Children = append(st.Children, cs)
	}
	if costErr != nil && st.Budget > 0 {
		return nil, fmt.Errorf("group %q has a budget but its cost is unreadable: %w", holding, costErr)
	}
	return st, nil
}

// CheckStart applies the U11 ceiling before a child starts: refused when
// the group is at its budget, or the child at its budget_share of it. A
// refusal is a rule, not a decision — no level lets it through.
func (e *Env) CheckStart(ctx context.Context, holding, child string) error {
	h, err := e.LoadHolding(holding)
	if err != nil {
		return err
	}
	var entry *orgdesign.ChildOrg
	for i := range h.ChildOrgs {
		if h.ChildOrgs[i].Org == child {
			entry = &h.ChildOrgs[i]
		}
	}
	if entry == nil {
		return fmt.Errorf("org %q is not a child of %q", child, holding)
	}
	budget := GroupBudget(h)
	if budget <= 0 {
		return nil
	}
	st, err := e.GroupStatus(ctx, holding)
	if err != nil {
		return err
	}
	if st.RollupUS >= budget {
		return fmt.Errorf("group %q has spent $%.2f of its $%.2f budget", holding, st.RollupUS, budget)
	}
	if entry.BudgetShare != nil {
		ceiling := *entry.BudgetShare * budget
		for _, c := range st.Children {
			if c.Org == child && c.CostUSD >= ceiling {
				return fmt.Errorf("%q has spent $%.2f of its $%.2f share", child, c.CostUSD, ceiling)
			}
		}
	}
	return nil
}

// reportUpTag marks the responsibility line mono-agent manages in a child
// boss so it is never duplicated and is removed when the org stops being a
// child.
const reportUpTag = "[managed:report-up]"

// ApplyReportUp inserts, updates, or removes each child boss's report-up
// line for the holding orgs in docs. It returns the docs it changed.
func ApplyReportUp(docs []*orgdesign.Doc) []*orgdesign.Doc {
	parentOf := map[string]*orgdesign.Doc{}
	for _, d := range docs {
		for _, c := range d.ChildOrgs {
			if _, taken := parentOf[c.Org]; !taken {
				parentOf[c.Org] = d
			}
		}
	}
	var changed []*orgdesign.Doc
	for _, d := range docs {
		boss, ok := d.RootRole()
		if !ok {
			continue
		}
		want := ""
		if p := parentOf[d.Name]; p != nil {
			if initiator, ok := p.RootRole(); ok {
				want = fmt.Sprintf("%s Before org_complete, org_send a short summary of what this org did and its outcome to %s:%s.", reportUpTag, p.Name, initiator.ID)
			}
		}
		var lines []string
		have := ""
		for _, l := range boss.Responsibilities {
			if strings.HasPrefix(l, reportUpTag) {
				have = l
				continue
			}
			lines = append(lines, l)
		}
		if have == want {
			continue
		}
		if want != "" {
			lines = append(lines, want)
		}
		if lines == nil {
			lines = []string{}
		}
		boss.Responsibilities = lines
		changed = append(changed, d)
	}
	return changed
}

// InitiatorOrgTools builds the org-tool grant for a holding org's Initiator.
func InitiatorOrgTools(h *orgdesign.Doc) []orggrant.OrgTool {
	var children []string
	for _, c := range h.ChildOrgs {
		children = append(children, c.Org)
	}
	out := make([]orggrant.OrgTool, 0, len(InitiatorTools))
	for _, t := range InitiatorTools {
		out = append(out, orggrant.OrgTool{Tool: t, Orgs: children})
	}
	return out
}
