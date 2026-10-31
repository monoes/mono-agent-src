package agentpurposes

import (
	"slices"
	"strings"
)

// Scope is what a role category may be used for: the purpose its work
// defaults to, and the purposes a parent may narrow it to. It is derived from
// the role, never from anything an agent says.
type Scope struct {
	Default string
	Allowed []string // includes Default
}

// scopes maps every role category the built-in roles carry (dynorg/roles.go,
// roles_app.go) and every purpose's own category. A category absent here
// (a custom or library role) is unmapped.
var scopes = map[string]Scope{
	"engineering":   {"coding", []string{"coding", "debugging"}},
	"debug":         {"debugging", []string{"debugging", "coding"}},
	"research":      {"research", []string{"research", "deep-research"}},
	"deep-research": {"deep-research", []string{"deep-research", "research"}},
	"planning":      {"planning", []string{"planning", "strategy"}},
	"strategy":      {"strategy", []string{"strategy", "planning"}},
	"review":        {"review", []string{"review", "security-audit"}},
	"testing":       {"testing", []string{"testing"}},
	"security":      {"security-audit", []string{"security-audit", "review"}},
	"docs":          {"writing", []string{"writing", "translation"}},
	"analysis":      {"data", []string{"data"}},
	"automation":    {"automation", []string{"automation", "browsing"}},
	"browsing":      {"browsing", []string{"browsing", "automation"}},
	"design":        {"design", []string{"design"}},
	"triage":        {"triage", []string{"triage"}},
	"judging":       {"judging", []string{"judging"}},
	"orchestrating": {"orchestrating", []string{"orchestrating"}},
	"translation":   {"translation", []string{"translation", "writing"}},
	"operations":    {"automation", []string{"automation", "triage"}},
}

// ScopeFor is the scope of a role category: the exact category, else the
// longest mapped name it contains ("backend-engineering"). ok is false for a
// category that maps to nothing.
func ScopeFor(category string) (Scope, bool) {
	c := strings.ToLower(strings.TrimSpace(category))
	if sc, ok := scopes[c]; ok {
		return sc, true
	}
	best, bestLen := "", 0
	for name := range scopes {
		if c != "" && strings.Contains(c, name) && len(name) > bestLen {
			best, bestLen = name, len(name)
		}
	}
	if best == "" {
		return Scope{}, false
	}
	return scopes[best], true
}

// Allows reports whether a parent may narrow the work to the purpose.
func (sc Scope) Allows(id string) bool { return slices.Contains(sc.Allowed, id) }
