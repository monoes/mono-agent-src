package apipkg

import (
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/connections"
)

// Package credentials with a scope_group are checked against the umbrella
// google/microsoft connection's granted scopes.
func init() { ScopeCheck = connections.CheckCredentialScopes }

// scopeChecker checks, once per scope group, that the run's credential
// grants the group an operation needs: the op's scope_group, else the
// selected credential's.
type scopeChecker struct {
	pkg     *Package
	cred    map[string]any
	credDef *Credential
	done    map[string]error
}

func (s *scopeChecker) check(op *Operation) error {
	group := op.ScopeGroup
	if group == "" && s.credDef != nil {
		group = s.credDef.ScopeGroup
	}
	if group == "" || ScopeCheck == nil {
		return nil
	}
	if s.done == nil {
		s.done = map[string]error{}
	}
	err, seen := s.done[group]
	if !seen {
		if err = ScopeCheck(s.cred, group); err != nil {
			err = fmt.Errorf("apipkg %s %s: %w", s.pkg.ID, op.Key(), err)
		}
		s.done[group] = err
	}
	return err
}

// checkScopeGroups validates credential and operation scope_group names
// against the scope groups of the package's umbrella credentials
// (reuse_platform google/microsoft). Without one the check never runs, so
// names are not judged.
func (p *Package) checkScopeGroups() []string {
	valid := map[string]bool{}
	var umbrellas []string
	for _, c := range p.Credentials {
		if !connections.IsUmbrella(c.ReusePlatform) {
			continue
		}
		umbrellas = append(umbrellas, c.ReusePlatform)
		for _, g := range connections.ScopeGroups(c.ReusePlatform) {
			valid[g.Name] = true
		}
	}
	if len(umbrellas) == 0 {
		return nil // the check only judges umbrella connections
	}
	var probs []string
	bad := func(where, group string) {
		var names []string
		for _, u := range umbrellas {
			for _, g := range connections.ScopeGroups(u) {
				names = append(names, g.Name)
			}
		}
		probs = append(probs, fmt.Sprintf("%s: unknown scope_group %q (valid: %s)", where, group, strings.Join(names, ", ")))
	}
	for i, c := range p.Credentials {
		if c.ScopeGroup != "" && connections.IsUmbrella(c.ReusePlatform) && !valid[c.ScopeGroup] {
			bad(fmt.Sprintf("credentials[%d] (%s)", i, c.Platform), c.ScopeGroup)
		}
	}
	for i := range p.Operations {
		o := &p.Operations[i]
		if o.ScopeGroup != "" && !valid[o.ScopeGroup] {
			bad(fmt.Sprintf("operations[%d] (%s)", i, o.Key()), o.ScopeGroup)
		}
	}
	return probs
}
