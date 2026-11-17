package monomind

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// runIDRe keeps a run id one path segment that cannot read as a flag (the
// documents reader applies the same rule).
var runIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Section budgets (monomind 2.24, orgrt/documents/section-budget*.ts).
//
// monomind 2.24.1 exposes no per-section spend: `org status --format json`
// has no section data, and `org costs` / `org report --format json` carry
// no section breakdown (the section table is text-only in `org report`).
// monomind 2.29 adds a `sections` field to `org report --format json`;
// OrgBudget cross-checks against it (org_budget_check.go).
// What it does put on the bus are the raw inputs and its own verdicts:
//
//	usage  events  data.cost_usd (null = unknown, never $0), data.tokens, from=<role>
//	audit  events  reason section-budget-warning | section-budget-closed |
//	               section-budget-reopened, data {scope, spentUsd, allocationUsd, ...}
//
// SectionBudgets is the ONE place the per-section breakdown is derived. It
// mirrors monomind's own `partitionOf` + `allocationStatus` (offline
// variant, which `org report` uses: each role's spend summed from the run's
// usage events), so the org total equals the sum of every usage event's
// cost_usd, i.e. `org report`'s "Cost" and `org costs`' total. The
// closure moment and its roles are NOT recomputed: they are read from
// monomind's own audit events. Nothing here invents a field.

// BudgetWarnFraction is monomind's SECTION_BUDGET_WARN_FRACTION.
const BudgetWarnFraction = 0.8

// BudgetState is monomind's ClosureState.
type BudgetState string

const (
	BudgetUnallocated BudgetState = "unallocated" // nothing to measure against
	BudgetOK          BudgetState = "ok"
	BudgetWarn        BudgetState = "warn"   // from 80 percent
	BudgetClosed      BudgetState = "closed" // at or above the allocation
)

// Budget scope kinds, monomind's `section` | `reserve` | `org`.
const (
	ScopeSection = "section"
	ScopeReserve = "reserve"
	ScopeOrg     = "org"
)

// RoleBudget is one role's spend against its own cap (`budget_usd`).
type RoleBudget struct {
	ID          string      `json:"id"`
	SpentUSD    float64     `json:"spent_usd"`
	Tokens      int64       `json:"tokens"`
	CapUSD      *float64    `json:"cap_usd,omitempty"`
	Fraction    *float64    `json:"fraction,omitempty"`
	State       BudgetState `json:"state"`
	CostUnknown bool        `json:"cost_unknown,omitempty"` // a usage event carried no cost
}

// ScopeBudget is a section, the root reserve or the org.
type ScopeBudget struct {
	Kind          string       `json:"kind"`
	Name          string       `json:"name,omitempty"`
	Lead          string       `json:"lead,omitempty"`
	AllocationUSD *float64     `json:"allocation_usd,omitempty"`
	RoleCapSumUSD float64      `json:"role_cap_sum_usd"`
	SpentUSD      float64      `json:"spent_usd"`
	RemainingUSD  *float64     `json:"remaining_usd,omitempty"`
	Fraction      *float64     `json:"fraction,omitempty"`
	State         BudgetState  `json:"state"`
	Roles         []RoleBudget `json:"roles,omitempty"`

	// From monomind's audit events (empty when it emitted none).
	WarnedAt    int64    `json:"warned_at,omitempty"` // event ts, ms
	ClosedAt    int64    `json:"closed_at,omitempty"` // the soft-closure moment, ms
	SoftClosed  bool     `json:"soft_closed,omitempty"`
	ClosedRoles []string `json:"closed_roles,omitempty"`
	HeldTasks   []string `json:"held_tasks,omitempty"`
	ReopenedAt  int64    `json:"reopened_at,omitempty"`
	CostUnknown bool     `json:"cost_unknown,omitempty"`
}

// BudgetReport is the per-section breakdown of one run.
type BudgetReport struct {
	Org      string        `json:"org"`
	Run      string        `json:"run,omitempty"`
	Sections []ScopeBudget `json:"sections"`
	Reserve  *ScopeBudget  `json:"reserve,omitempty"`
	Total    ScopeBudget   `json:"total"`
	// Tokens across every usage event: the same total `org report` prints.
	TotalTokens int64 `json:"total_tokens"`
	// CostComplete is false when any usage event carried no cost: the totals
	// are then lower bounds (monomind's RunSummary.costComplete).
	CostComplete bool `json:"cost_complete"`
	// UnattributedUSD is spend of roles the definition does not have.
	UnattributedUSD float64 `json:"unattributed_usd,omitempty"`
	// Check compares these numbers with monomind's own `org report` rows
	// (monomind 2.29+); set by OrgBudget only.
	Check *ReportCheck `json:"check,omitempty"`
}

type budgetRole struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Kind      string   `json:"kind"`
	ReportsTo *string  `json:"reports_to"`
	BudgetUSD *float64 `json:"budget_usd"`
	Policy    struct {
		MaxUSD *float64 `json:"maxUsd"`
	} `json:"policy"`
}

type budgetDef struct {
	Roles     []budgetRole               `json:"roles"`
	Sections  map[string]json.RawMessage `json:"sections"`
	RunConfig struct {
		BudgetUSD *float64 `json:"budget_usd"`
	} `json:"run_config"`
	order []string
}

func positive(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 {
		return nil
	}
	return v
}

func round6(n float64) float64 { return math.Round(n*1e6) / 1e6 }

// roleCap is monomind's effectiveRoleUsdCap: budget_usd, with policy.maxUsd
// only allowed when equal. A conflicting or invalid cap resolves to none.
func (r budgetRole) cap() *float64 {
	c := positive(r.BudgetUSD)
	if r.BudgetUSD != nil && c == nil {
		return nil
	}
	if r.Policy.MaxUSD != nil && (positive(r.Policy.MaxUSD) == nil || r.BudgetUSD == nil || *r.Policy.MaxUSD != *r.BudgetUSD) {
		return nil
	}
	return c
}

// sectionOrder returns the keys of the `sections` object in file order.
func sectionOrder(raw []byte) []string {
	var probe struct {
		Sections json.RawMessage `json:"sections"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Sections) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(probe.Sections))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			break
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			break
		}
	}
	return keys
}

type spend struct {
	usd     float64
	tokens  int64
	unknown bool
}

// SectionBudgets derives the per-section breakdown from an org definition
// (the org's .monomind/orgs/<name>.json) and the run's bus events (NDJSON
// lines, as `org events` prints them). Unparseable lines are skipped.
func SectionBudgets(def []byte, events [][]byte) (*BudgetReport, error) {
	var d budgetDef
	if err := json.Unmarshal(def, &d); err != nil {
		return nil, err
	}
	d.order = sectionOrder(def)

	per := map[string]*spend{}
	var totalTokens int64
	complete := true
	closure := map[string]*ScopeBudget{} // keyed by scope, filled from audit events
	var org, run string
	for _, line := range events {
		var ev struct {
			TS   int64  `json:"ts"`
			Org  string `json:"org"`
			Run  string `json:"run"`
			Type string `json:"type"`
			From string `json:"from"`
			Rsn  string `json:"reason"`
			Data struct {
				Tokens  *int64   `json:"tokens"`
				Cost    *float64 `json:"cost_usd"`
				Scope   string   `json:"scope"`
				Closed  []string `json:"closed"`
				Held    []string `json:"held"`
				Reopens []string `json:"reopened"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if org == "" {
			org, run = ev.Org, ev.Run
		}
		switch {
		case ev.Type == "usage" && ev.From != "":
			s := per[ev.From]
			if s == nil {
				s = &spend{}
				per[ev.From] = s
			}
			if ev.Data.Tokens != nil {
				s.tokens += *ev.Data.Tokens
				totalTokens += *ev.Data.Tokens
			}
			if ev.Data.Cost != nil && !math.IsNaN(*ev.Data.Cost) {
				s.usd += *ev.Data.Cost
			} else {
				s.unknown, complete = true, false
			}
		case ev.Type == "audit" && ev.Data.Scope != "":
			c := closure[ev.Data.Scope]
			if c == nil {
				c = &ScopeBudget{}
				closure[ev.Data.Scope] = c
			}
			switch ev.Rsn {
			case "section-budget-warning":
				c.WarnedAt = ev.TS
			case "section-budget-closed":
				c.ClosedAt, c.SoftClosed, c.ReopenedAt = ev.TS, true, 0
				c.ClosedRoles, c.HeldTasks = ev.Data.Closed, ev.Data.Held
			case "section-budget-reopened":
				c.ReopenedAt, c.SoftClosed = ev.TS, false
			}
		}
	}

	// partitionOf: roster = members + lead (once each); the root, a role
	// already homed and an endpoint role stay out; everyone else is reserve.
	byID := map[string]budgetRole{}
	var root string
	for _, r := range d.Roles {
		byID[r.ID] = r
		if root == "" && r.Type == "boss" {
			root = r.ID
		}
	}
	if root == "" {
		for _, r := range d.Roles {
			if r.ReportsTo == nil {
				root = r.ID
				break
			}
		}
	}
	homed := map[string]bool{}
	known := map[string]bool{}
	scope := func(kind, name, lead string, alloc *float64, ids []string) ScopeBudget {
		sc := ScopeBudget{Kind: kind, Name: name, Lead: lead, AllocationUSD: alloc}
		for _, id := range ids {
			known[id] = true
			r := byID[id]
			s := per[id]
			if s == nil {
				s = &spend{}
			}
			rb := RoleBudget{ID: id, SpentUSD: round6(s.usd), Tokens: s.tokens, CapUSD: r.cap(), CostUnknown: s.unknown}
			rb.State = stateOf(s.usd, rb.CapUSD)
			if rb.CapUSD != nil {
				f := s.usd / *rb.CapUSD
				rb.Fraction = &f
				sc.RoleCapSumUSD += *rb.CapUSD
			}
			sc.SpentUSD += s.usd
			sc.CostUnknown = sc.CostUnknown || s.unknown
			sc.Roles = append(sc.Roles, rb)
		}
		sc.RoleCapSumUSD, sc.SpentUSD = round6(sc.RoleCapSumUSD), round6(sc.SpentUSD)
		return sc
	}

	rep := &BudgetReport{Org: org, Run: run, Sections: []ScopeBudget{}, TotalTokens: totalTokens, CostComplete: complete}
	var allocated float64
	for _, name := range d.order {
		var sec struct {
			Lead    string          `json:"lead"`
			Members []string        `json:"members"`
			Budget  json.RawMessage `json:"budget"`
		}
		if json.Unmarshal(d.Sections[name], &sec) != nil {
			continue
		}
		var alloc *float64
		var b map[string]float64
		if json.Unmarshal(sec.Budget, &b) == nil && len(b) == 1 {
			if v, ok := b["usd"]; ok {
				alloc = positive(&v)
			}
		}
		var ids []string
		seen := map[string]bool{}
		for _, id := range append(append([]string{}, sec.Members...), sec.Lead) {
			r, ok := byID[id]
			if id == "" || seen[id] || !ok || id == root || homed[id] || r.Kind == "endpoint" {
				continue
			}
			seen[id], homed[id] = true, true
			ids = append(ids, id)
		}
		if alloc != nil {
			allocated += *alloc
		}
		sc := scope(ScopeSection, name, sec.Lead, alloc, ids)
		sc.finish(closure[ScopeSection+":"+name])
		rep.Sections = append(rep.Sections, sc)
	}
	var reserveIDs []string
	for _, r := range d.Roles {
		if r.Kind != "endpoint" && !homed[r.ID] {
			reserveIDs = append(reserveIDs, r.ID)
		}
	}
	orgUSD := positive(d.RunConfig.BudgetUSD)
	var reserveUSD *float64
	if orgUSD != nil {
		v := math.Max(0, round6(*orgUSD-allocated))
		reserveUSD = &v
	}
	res := scope(ScopeReserve, "", "", reserveUSD, reserveIDs)
	res.finish(closure["reserve"])
	if len(reserveIDs) > 0 {
		rep.Reserve = &res
	}

	var spent, capSum float64
	for _, s := range rep.Sections {
		spent += s.SpentUSD
		capSum += s.RoleCapSumUSD
	}
	spent += res.SpentUSD
	capSum += res.RoleCapSumUSD
	total := ScopeBudget{Kind: ScopeOrg, AllocationUSD: orgUSD, RoleCapSumUSD: round6(capSum), CostUnknown: !complete}
	// A role the definition no longer has counts in the org total and in no section.
	var unattributed float64
	for id, s := range per {
		if !known[id] {
			unattributed += s.usd
		}
	}
	rep.UnattributedUSD = round6(unattributed)
	total.SpentUSD = round6(spent + unattributed)
	total.finish(closure[ScopeOrg])
	rep.Total = total
	return rep, nil
}

func stateOf(spent float64, alloc *float64) BudgetState {
	switch {
	case alloc == nil:
		return BudgetUnallocated
	case spent >= *alloc-1e-9:
		return BudgetClosed
	case spent >= *alloc*BudgetWarnFraction:
		return BudgetWarn
	}
	return BudgetOK
}

// finish fills the derived fields and merges monomind's closure events.
func (s *ScopeBudget) finish(ev *ScopeBudget) {
	s.State = stateOf(s.SpentUSD, s.AllocationUSD)
	if s.AllocationUSD != nil {
		r := round6(*s.AllocationUSD - s.SpentUSD)
		s.RemainingUSD = &r
		// The root reserve can be zero when sections use the entire org
		// allocation. A percentage has no meaning then; omit it rather
		// than producing NaN/Inf, which cannot be encoded as JSON.
		if *s.AllocationUSD > 0 {
			f := s.SpentUSD / *s.AllocationUSD
			s.Fraction = &f
		}
	}
	if ev != nil {
		s.WarnedAt, s.ClosedAt, s.ReopenedAt, s.SoftClosed = ev.WarnedAt, ev.ClosedAt, ev.ReopenedAt, ev.SoftClosed
		s.ClosedRoles, s.HeldTasks = ev.ClosedRoles, ev.HeldTasks
	}
}

// SplitEventLines splits NDJSON into its non-empty lines.
func SplitEventLines(b []byte) [][]byte {
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			out = append(out, append([]byte(nil), l...))
		}
	}
	return out
}

// OrgBudget reads an org's definition and one run's bus (`org events`, the
// current run when run is empty) and derives its per-section breakdown.
func OrgBudget(ctx context.Context, projectRoot, name, run string) (*BudgetReport, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("invalid org name %q", name)
	}
	if run != "" && (!runIDRe.MatchString(run) || strings.Contains(run, "..")) {
		return nil, fmt.Errorf("invalid run id %q", run)
	}
	def, err := os.ReadFile(filepath.Join(projectRoot, ".monomind", "orgs", name+".json"))
	if err != nil {
		return nil, fmt.Errorf("read org definition: %w", err)
	}
	var lines [][]byte
	err = OrgEvents(ctx, projectRoot, name, OrgEventsOptions{Run: run}, func(l []byte) { lines = append(lines, l) })
	if err != nil {
		return nil, err
	}
	rep, err := SectionBudgets(def, lines)
	if err != nil {
		return nil, err
	}
	attachCheck(ctx, rep, projectRoot, name, run)
	return rep, nil
}
