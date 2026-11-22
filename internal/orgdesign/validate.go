package orgdesign

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ValidationError carries every structural problem found by Validate, so
// callers can show/report them all at once rather than one-at-a-time.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return "org config invalid: " + strings.Join(e.Errors, "; ")
}

// Validate reproduces every check in monomind's own checkOrgStructure
// (packages/@monomind/cli/src/orgrt/migrate.ts) — unique role ids, exactly
// one root role, every reports_to resolving to an existing id, no
// self-report — PLUS full cycle detection, which checkOrgStructure omits.
// Confirmed empirically: a 3-role chain b->c->b with a separate valid root
// passes `monomind org validate` with exit 0. This package's Save refuses
// to write what monomind's own validator would silently accept.
func Validate(d *Doc) error {
	var errs []string

	if d.Name == "" {
		errs = append(errs, "org name is required")
	} else if !ValidOrgName(d.Name) {
		errs = append(errs, fmt.Sprintf("invalid org name: %q", d.Name))
	}

	if len(d.Roles) == 0 {
		errs = append(errs, "org must have at least one role")
	}

	seen := make(map[string]bool, len(d.Roles))
	var dupes []string
	for _, r := range d.Roles {
		if r.ID == "" {
			errs = append(errs, "a role has an empty id")
			continue
		}
		if strings.ContainsAny(r.ID, `/\:`) || r.ID == "." || r.ID == ".." {
			// The id is a path segment (worktree-<id>) and half of org:role.
			errs = append(errs, fmt.Sprintf("invalid role id %q: no path separators, colons, or dot segments", r.ID))
		}
		if seen[r.ID] {
			dupes = append(dupes, r.ID)
		}
		seen[r.ID] = true
	}
	if len(dupes) > 0 {
		errs = append(errs, fmt.Sprintf("duplicate role id(s): %s", strings.Join(uniqueStrings(dupes), ", ")))
	}

	var roots []string
	for _, r := range d.Roles {
		if r.ReportsTo == nil {
			roots = append(roots, r.ID)
		}
	}
	switch {
	case len(roots) == 0 && len(d.Roles) > 0:
		errs = append(errs, "no root role — exactly one role must have reports_to: null")
	case len(roots) > 1:
		errs = append(errs, fmt.Sprintf("multiple root roles (%s) — exactly one may have reports_to: null", strings.Join(roots, ", ")))
	}

	// "boss" is derived from tree position, not a freely-typed label (see
	// UpdateRole's boss-guard and PromoteToRoot) — this catches any
	// regression of that invariant (e.g. a config hand-edited outside the
	// app, or a bug that lets a stray type:"boss" through) rather than
	// silently reproducing monomind daemon's ambiguous first-match-wins
	// entrypoint selection when more than one role claims it.
	var bossIDs []string
	for _, r := range d.Roles {
		if r.Type == "boss" {
			bossIDs = append(bossIDs, r.ID)
		}
	}
	if len(bossIDs) > 1 {
		errs = append(errs, fmt.Sprintf(`multiple roles with type "boss" (%s) — exactly one role (the root) may be "boss"`, strings.Join(bossIDs, ", ")))
	} else if len(bossIDs) == 1 && len(roots) == 1 && bossIDs[0] != roots[0] {
		errs = append(errs, fmt.Sprintf(`role %q has type "boss" but is not the org root (%q is) — "boss" must be the root role`, bossIDs[0], roots[0]))
	}

	for _, r := range d.Roles {
		if r.ReportsTo == nil {
			continue
		}
		if *r.ReportsTo == r.ID {
			errs = append(errs, fmt.Sprintf("role %q reports to itself", r.ID))
			continue
		}
		if !seen[*r.ReportsTo] {
			errs = append(errs, fmt.Sprintf("role %q: reports_to %q matches no role id", r.ID, *r.ReportsTo))
		}
	}

	for _, cyc := range DetectCycles(d.Roles) {
		errs = append(errs, fmt.Sprintf("circular reporting: %s", strings.Join(cyc, " -> ")))
	}

	if raw, ok := d.RunConfig["workspace"]; ok {
		var ws string
		if json.Unmarshal(raw, &ws) == nil && ws != "" && !filepath.IsAbs(ws) {
			if c := filepath.Clean(ws); c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
				errs = append(errs, fmt.Sprintf("run_config.workspace %q climbs out of the project root", ws))
			}
		}
	}

	errs = append(errs, validateUnification(d)...)
	errs = append(errs, validateSections(d)...)

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

// DetectCycles returns every cycle found in the reports_to graph, each as
// the ordered list of role ids on that cycle (closing back on the first
// id). reports_to is a functional graph — each node has at most one parent
// — so this is a single O(n) three-color walk per unvisited node: white
// (unvisited), grey (on the current walk-up path), black (fully resolved,
// known cycle-free). Hitting grey means the current path closes a cycle.
func DetectCycles(roles []Role) [][]string {
	byID := make(map[string]*Role, len(roles))
	for i := range roles {
		byID[roles[i].ID] = &roles[i]
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(roles))
	var cycles [][]string

	var walk func(startID string)
	walk = func(startID string) {
		var path []string
		id := startID
		for {
			if id == "" || byID[id] == nil {
				break
			}
			switch color[id] {
			case black:
				// Reached an already-resolved, cycle-free node — the whole
				// path we just walked is cycle-free too.
				for _, p := range path {
					color[p] = black
				}
				return
			case grey:
				// id is on our current path — everything from id onward in
				// path forms the cycle.
				start := indexOf(path, id)
				cyc := append(append([]string{}, path[start:]...), id)
				cycles = append(cycles, cyc)
				for _, p := range path {
					if color[p] != black {
						color[p] = black
					}
				}
				return
			}
			color[id] = grey
			path = append(path, id)
			r := byID[id]
			if r.ReportsTo == nil {
				break
			}
			id = *r.ReportsTo
		}
		for _, p := range path {
			if color[p] != black {
				color[p] = black
			}
		}
	}

	for _, r := range roles {
		if color[r.ID] == white {
			walk(r.ID)
		}
	}
	return cycles
}

// WouldCycle answers the canvas's and the AI tool's actual question — "may
// childID report to newParentID?" — without mutating anything: walk up
// newParentID's own reports_to chain; if childID appears (or newParentID ==
// childID), assigning the edge would close a cycle.
func WouldCycle(roles []Role, childID, newParentID string) bool {
	if childID == newParentID {
		return true
	}
	byID := make(map[string]*Role, len(roles))
	for i := range roles {
		byID[roles[i].ID] = &roles[i]
	}
	visited := make(map[string]bool)
	id := newParentID
	for id != "" {
		if id == childID {
			return true
		}
		if visited[id] {
			// Already-broken graph (a pre-existing cycle elsewhere) — bail
			// rather than loop forever; Validate will separately report it.
			return false
		}
		visited[id] = true
		r, ok := byID[id]
		if !ok || r.ReportsTo == nil {
			return false
		}
		id = *r.ReportsTo
	}
	return false
}

// UniqueRoleID slugifies desired into a role id and, if it collides with an
// existing role in d, appends -2, -3, ... until it doesn't. Empty or
// fully-unsluggable input falls back to "role". Callers pass a human title
// (e.g. "Security Auditor" -> "security-auditor"); an explicit
// caller-supplied id is never passed through here — see AddRole.
func UniqueRoleID(d *Doc, desired string) string {
	base := slugify(desired)
	if base == "" {
		base = "role"
	}
	existing := make(map[string]bool, len(d.Roles))
	for _, r := range d.Roles {
		existing[r.ID] = true
	}
	if !existing[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out != "" && (out[0] < 'a' || out[0] > 'z') {
		out = "r-" + out
	}
	return out
}

func indexOf(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
