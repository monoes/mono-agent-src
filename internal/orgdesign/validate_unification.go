package orgdesign

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// aliasRe bounds an automation alias: it becomes the MCP tool name
// automation_<alias> and a decision class grant:<alias>, so it stays in the
// lowercase-underscore alphabet both accept.
var aliasRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// providerNameRe mirrors monomind's ToolProviderSchema.name.
var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// reservedAliases may not be used as automation aliases: "human" and
// "workflow" are message senders, and "status"/"output" would collide with
// the grant-mode tools automation_status and automation_output (C-15).
var reservedAliases = map[string]bool{
	"human": true, "workflow": true, "status": true, "output": true,
}

// ValidAlias reports whether alias is usable as an automation alias.
func ValidAlias(alias string) bool {
	return aliasRe.MatchString(alias) && !reservedAliases[alias]
}

// Autonomy vocabulary (plan §7.7, contracts §8).
const (
	LevelManual = "manual"
	LevelMid    = "mid"
	LevelFull   = "full"

	TierRoutine       = "routine"
	TierConsequential = "consequential"
	TierIrreversible  = "irreversible"

	DeciderModel  = "model"
	DeciderBoss   = "boss"
	DeciderParent = "parent"
	// DeciderJev asks TypeSafe Jev to pick the verdict and hands anything it
	// is not confident about to the model decider (jev plan WS4).
	DeciderJev = "jev"
)

// DeciderKinds lists every decider kind, in display order.
var DeciderKinds = []string{DeciderModel, DeciderBoss, DeciderParent, DeciderJev}

// ValidDeciderKind reports whether kind is one of DeciderKinds.
func ValidDeciderKind(kind string) bool {
	for _, k := range DeciderKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// deciderProviderRe is a decision provider's name (jevconf.Provider.Name).
var deciderProviderRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidDeciderProvider reports whether name is shaped like a decision
// provider's name ("typesafe" or a configured one). Whether it exists is
// checked against the providers when the enforced settings are set.
func ValidDeciderProvider(name string) bool {
	return deciderProviderRe.MatchString(name)
}

// LevelRank orders levels by how much they let run without a human; -1 for
// an unknown level.
func LevelRank(level string) int {
	switch level {
	case LevelManual:
		return 0
	case LevelMid:
		return 1
	case LevelFull:
		return 2
	}
	return -1
}

// ValidTier reports whether tier is a known decision tier.
func ValidTier(tier string) bool {
	return tier == TierRoutine || tier == TierConsequential || tier == TierIrreversible
}

var classNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidDecisionClass reports whether class names a decision class:
// tool:<Name>, org_complete, question, grant:<alias>, org_start, gate,
// hil:<alias>, plus the wildcards tool:* and grant:*.
func ValidDecisionClass(class string) bool {
	switch class {
	case "org_complete", "question", "org_start", "gate", "tool:*", "grant:*":
		return true
	}
	for _, p := range []string{"tool:", "grant:", "hil:"} {
		if strings.HasPrefix(class, p) {
			return classNameRe.MatchString(strings.TrimPrefix(class, p))
		}
	}
	return false
}

// validateUnification checks every key added by the org × workflow
// unification plan (§6.1). It is structural only: facts that need the
// database (workflow exists in this profile, grant ids exist) are checked by
// internal/orggrant, and facts that span several org files by
// ValidateProfileOrgs.
func validateUnification(d *Doc) []string {
	var errs []string

	switch d.Kind {
	case "", OrgKindStandard, OrgKindHolding:
	default:
		errs = append(errs, fmt.Sprintf("kind %q must be %q or %q", d.Kind, OrgKindStandard, OrgKindHolding))
	}

	roleIDs := map[string]bool{}
	for _, r := range d.Roles {
		roleIDs[r.ID] = true
	}

	aliases := map[string]bool{}
	workflowIDs := map[string]bool{}
	for _, a := range d.Automations {
		switch {
		case !ValidAlias(a.Alias):
			errs = append(errs, fmt.Sprintf("automations: alias %q must match %s and not be one of human, workflow, status, output", a.Alias, aliasRe))
		case aliases[a.Alias]:
			errs = append(errs, fmt.Sprintf("automations: duplicate alias %q", a.Alias))
		case roleIDs[a.Alias]:
			errs = append(errs, fmt.Sprintf("automations: alias %q is also a role id", a.Alias))
		}
		aliases[a.Alias] = true
		if a.WorkflowID == "" {
			errs = append(errs, fmt.Sprintf("automations: %q has no workflow_id", a.Alias))
		}
		workflowIDs[a.WorkflowID] = true
	}

	// One workflow may back only one automation role. Two roles on the same
	// workflow are indistinguishable at run time: automationRoleFor returns
	// the first match, so every org.send/org.ask from that workflow is
	// stamped as the first role and org.ask's return address is that role —
	// a reply to the second role never matches the ask and it times out.
	endpointWorkflows := map[string]string{}
	for i := range d.Roles {
		r := &d.Roles[i]
		errs = append(errs, validateRoleUnification(d, r, aliases, workflowIDs)...)
		if !r.IsEndpoint() || r.Automation == nil || r.Automation.WorkflowID == "" {
			continue
		}
		if first, ok := endpointWorkflows[r.Automation.WorkflowID]; ok {
			errs = append(errs, fmt.Sprintf("role %q: workflow %q already runs automation role %q — an automation role needs a workflow of its own", r.ID, r.Automation.WorkflowID, first))
		} else {
			endpointWorkflows[r.Automation.WorkflowID] = r.ID
		}
	}

	if d.IsHolding() {
		if len(d.ChildOrgs) == 0 {
			errs = append(errs, "a holding org needs at least one entry in children")
		}
	} else if len(d.ChildOrgs) > 0 {
		errs = append(errs, `children is only allowed when kind is "holding"`)
	}
	seenChild := map[string]bool{}
	for _, c := range d.ChildOrgs {
		switch {
		case !ValidOrgName(c.Org):
			errs = append(errs, fmt.Sprintf("children: invalid org name %q", c.Org))
		case c.Org == d.Name:
			errs = append(errs, "children: an org cannot be its own child")
		case seenChild[c.Org]:
			errs = append(errs, fmt.Sprintf("children: %q listed twice", c.Org))
		}
		seenChild[c.Org] = true
		switch c.Start {
		case "", "on_demand", "with_parent", "manual":
		default:
			errs = append(errs, fmt.Sprintf("children: %q start %q must be on_demand, with_parent, or manual", c.Org, c.Start))
		}
		if c.BudgetShare != nil && (*c.BudgetShare < 0 || *c.BudgetShare > 1) {
			errs = append(errs, fmt.Sprintf("children: %q budget_share must be between 0 and 1", c.Org))
		}
	}

	if f := d.Federation; f != nil {
		for _, list := range [][]string{f.AllowFrom, f.AllowTo} {
			for _, o := range list {
				if o != "*" && !ValidOrgName(o) {
					errs = append(errs, fmt.Sprintf("federation: %q is not an org name or *", o))
				}
			}
		}
	}

	if a := d.Autonomy; a != nil {
		errs = append(errs, validateAutonomy(a)...)
	}
	return errs
}

func validateRoleUnification(d *Doc, r *Role, aliases, workflowIDs map[string]bool) []string {
	var errs []string
	switch r.Kind {
	case "", RoleKindAgent, RoleKindEndpoint:
	default:
		errs = append(errs, fmt.Sprintf("role %q: kind %q must be %q or %q", r.ID, r.Kind, RoleKindAgent, RoleKindEndpoint))
	}

	if r.IsEndpoint() {
		if r.ReportsTo == nil {
			errs = append(errs, fmt.Sprintf("role %q: an automation role cannot be the org root", r.ID))
		}
		for _, k := range []string{"policy", "runtime", "adapter_config", "budget_tokens", "budget_usd"} {
			present := r.Extra[k] != nil
			if k == "adapter_config" {
				present = len(r.AdapterConfig) > 0
			}
			if present {
				errs = append(errs, fmt.Sprintf("role %q: an automation role may not set %s", r.ID, k))
			}
		}
		if len(r.ToolProviders) > 0 {
			errs = append(errs, fmt.Sprintf("role %q: an automation role may not set tool_providers", r.ID))
		}
		if len(r.Automations) > 0 {
			errs = append(errs, fmt.Sprintf("role %q: an automation role cannot hold grants", r.ID))
		}
		if r.Endpoint == nil || r.Endpoint.URL == "" {
			errs = append(errs, fmt.Sprintf("role %q: an automation role needs endpoint.url", r.ID))
		}
		if r.Automation == nil || r.Automation.WorkflowID == "" {
			errs = append(errs, fmt.Sprintf("role %q: an automation role needs automation.workflow_id", r.ID))
		} else {
			if !workflowIDs[r.Automation.WorkflowID] {
				errs = append(errs, fmt.Sprintf("role %q: workflow %q is not in this org's automations", r.ID, r.Automation.WorkflowID))
			}
			if rep := r.Automation.Reply; rep != "" && rep != "last_node" && !(strings.HasPrefix(rep, "node:") && len(rep) > len("node:")) {
				errs = append(errs, fmt.Sprintf("role %q: automation.reply %q must be last_node or node:<name>", r.ID, rep))
			}
		}
		if r.Type == "boss" {
			errs = append(errs, fmt.Sprintf("role %q: an automation role cannot be the boss", r.ID))
		}
	} else if r.Endpoint != nil || r.Automation != nil {
		errs = append(errs, fmt.Sprintf(`role %q: endpoint and automation are only allowed when kind is "endpoint"`, r.ID))
	}

	seen := map[string]bool{}
	for _, g := range r.Automations {
		if seen[g.Alias] {
			errs = append(errs, fmt.Sprintf("role %q: automation %q granted twice", r.ID, g.Alias))
		}
		seen[g.Alias] = true
		if !aliases[g.Alias] {
			errs = append(errs, fmt.Sprintf("role %q: automation %q is not in this org's automations", r.ID, g.Alias))
		}
		switch g.Mode {
		case "", "run", "trigger", "status":
		default:
			errs = append(errs, fmt.Sprintf("role %q: automation %q mode %q must be run, trigger, or status", r.ID, g.Alias, g.Mode))
		}
		switch g.Approval {
		case "", "none", "required":
		default:
			errs = append(errs, fmt.Sprintf("role %q: automation %q approval %q must be none or required", r.ID, g.Alias, g.Approval))
		}
		if g.TimeoutSeconds < 0 || g.MaxCallsPerRun < 0 || g.MaxCallsPerDay < 0 || g.MaxOutputBytes < 0 {
			errs = append(errs, fmt.Sprintf("role %q: automation %q has a negative limit", r.ID, g.Alias))
		}
	}

	providers := map[string]bool{}
	for _, p := range r.ToolProviders {
		if p.Kind != "mcp-stdio" {
			errs = append(errs, fmt.Sprintf("role %q: tool provider %q kind must be mcp-stdio", r.ID, p.Name))
		}
		if !providerNameRe.MatchString(p.Name) {
			errs = append(errs, fmt.Sprintf("role %q: tool provider name %q must match %s", r.ID, p.Name, providerNameRe))
		}
		if providers[p.Name] {
			errs = append(errs, fmt.Sprintf("role %q: tool provider %q listed twice", r.ID, p.Name))
		}
		providers[p.Name] = true
		if p.Command == "" {
			errs = append(errs, fmt.Sprintf("role %q: tool provider %q has no command", r.ID, p.Name))
		}
	}
	return errs
}

func validateAutonomy(a *Autonomy) []string {
	var errs []string
	if a.Level != "" && LevelRank(a.Level) < 0 {
		errs = append(errs, fmt.Sprintf("autonomy: level %q must be manual, mid, or full", a.Level))
	}
	if dc := a.Decider; dc != nil {
		if dc.Kind != "" && !ValidDeciderKind(dc.Kind) {
			errs = append(errs, fmt.Sprintf("autonomy: decider.kind %q must be model, boss, parent, or jev", dc.Kind))
		}
		switch dc.Fallback {
		case "", DeciderModel:
		case DeciderBoss:
			errs = append(errs, "autonomy: decider.fallback cannot be boss")
		default:
			errs = append(errs, fmt.Sprintf("autonomy: decider.fallback %q must be model", dc.Fallback))
		}
		if dc.TimeoutSeconds < 0 {
			errs = append(errs, "autonomy: decider.timeout_seconds cannot be negative")
		}
		if dc.Provider != "" && !ValidDeciderProvider(dc.Provider) {
			errs = append(errs, fmt.Sprintf("autonomy: decider.provider %q must be 1 to 32 of a-z, 0-9 and -", dc.Provider))
		}
	}
	keys := make([]string, 0, len(a.Tiers))
	for k := range a.Tiers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !ValidDecisionClass(k) {
			errs = append(errs, fmt.Sprintf("autonomy: tiers key %q is not a decision class", k))
		}
		if !ValidTier(a.Tiers[k]) {
			errs = append(errs, fmt.Sprintf("autonomy: tier %q for %q must be routine, consequential, or irreversible", a.Tiers[k], k))
		}
	}
	switch a.OnDeciderFailure {
	case "", "deny", "human":
	default:
		errs = append(errs, fmt.Sprintf("autonomy: on_decider_failure %q must be deny or human", a.OnDeciderFailure))
	}
	if l := a.Limits; l != nil && (l.MaxDecisionsPerRun < 0 || l.MaxDeciderUSDPerRun < 0) {
		errs = append(errs, "autonomy: limits cannot be negative")
	}
	return errs
}

// ValidateProfileOrgs checks the rules that span several org files in one
// profile root: a child org belongs to at most one holding org (C-39), the
// holding graph has no cycles, every child exists, and decider.kind
// "parent" is only used by an org some holding org lists as a child.
// Returns one message per problem, keyed by nothing — callers print them.
func ValidateProfileOrgs(docs []*Doc) []string {
	var errs []string
	byName := map[string]*Doc{}
	for _, d := range docs {
		byName[d.Name] = d
	}
	owner := map[string]string{}
	names := make([]string, 0, len(docs))
	for _, d := range docs {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		d := byName[n]
		for _, c := range d.ChildOrgs {
			if _, ok := byName[c.Org]; !ok {
				errs = append(errs, fmt.Sprintf("org %q: child %q does not exist in this profile", d.Name, c.Org))
			}
			if prev, ok := owner[c.Org]; ok && prev != d.Name {
				errs = append(errs, fmt.Sprintf("org %q: child %q already belongs to holding org %q", d.Name, c.Org, prev))
				continue
			}
			owner[c.Org] = d.Name
		}
	}

	// Holding graph cycle check: the same three-colour walk as DetectCycles,
	// but over org → children edges.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var walk func(n string, path []string)
	walk = func(n string, path []string) {
		color[n] = grey
		path = append(path, n)
		if d := byName[n]; d != nil {
			for _, c := range d.ChildOrgs {
				switch color[c.Org] {
				case grey:
					start := indexOf(path, c.Org)
					cyc := append(append([]string{}, path[start:]...), c.Org)
					errs = append(errs, "circular holding orgs: "+strings.Join(cyc, " -> "))
				case white:
					if byName[c.Org] != nil {
						walk(c.Org, path)
					}
				}
			}
		}
		color[n] = black
	}
	for _, n := range names {
		if color[n] == white {
			walk(n, nil)
		}
	}

	for _, n := range names {
		d := byName[n]
		if d.Autonomy != nil && d.Autonomy.Decider != nil && d.Autonomy.Decider.Kind == DeciderParent {
			if _, ok := owner[d.Name]; !ok {
				errs = append(errs, fmt.Sprintf(`org %q: decider "parent" needs a holding org that lists it in children`, d.Name))
			}
		}
	}
	return errs
}

// HoldingOwner returns the name of the holding org that lists child, or "".
func HoldingOwner(docs []*Doc, child string) string {
	for _, d := range docs {
		for _, c := range d.ChildOrgs {
			if c.Org == child {
				return d.Name
			}
		}
	}
	return ""
}

// LoadAll loads every org config under profileRoot, skipping (and
// returning) files that fail to parse.
func LoadAll(profileRoot string) ([]*Doc, map[string]error, error) {
	names, err := ListOrgNames(profileRoot)
	if err != nil {
		return nil, nil, err
	}
	var docs []*Doc
	bad := map[string]error{}
	for _, n := range names {
		d, err := Load(profileRoot, n)
		if err != nil {
			bad[n] = err
			continue
		}
		docs = append(docs, d)
	}
	return docs, bad, nil
}
