package dynorg

import (
	"fmt"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/agentpurposes"
)

// purposePolicy is how the operator's setting for a worker's purpose
// narrows its model choice (internal/agentpurposes). The zero value limits
// nothing.
type purposePolicy struct {
	id         string
	set        agentpurposes.Setting
	rule       string
	restricted bool   // the candidates are limited to the set's pairs
	author     string // a model the worker must differ from, "" = none
	notes      []string
}

// staffPurpose settles the worker's purpose: the lead's, which must be one
// of the built-in ids, else the one of the role's category ("" = none, the
// worker's models are not limited).
func (s *Staffer) staffPurpose(req SpawnRequest, st *Staff) error {
	id := strings.TrimSpace(req.Purpose)
	if id != "" {
		if _, ok := agentpurposes.Lookup(id); !ok {
			return fmt.Errorf("unknown purpose %q; leave purpose empty to derive it from the role, or use one of: %s", id, strings.Join(agentpurposes.IDs(), ", "))
		}
	} else {
		id = agentpurposes.ForCategory(st.Category)
	}
	st.Purpose = id
	return nil
}

// limitToPurpose applies the operator's setting for st.Purpose to the
// candidates: only pairs of the set that are validated (not stale) stay, a
// model that wrote the work is left out when the setting asks for another,
// and a set with nothing available fails the spawn unless the operator
// allowed the fallback. The check is in code: nothing the lead says widens it.
func (s *Staffer) limitToPurpose(req SpawnRequest, st *Staff, eligible *[]Model) (*purposePolicy, error) {
	pol := &purposePolicy{id: st.Purpose, rule: agentpurposes.RuleAny}
	if st.Purpose == "" {
		return pol, nil
	}
	set, chosen := s.Purposes[st.Purpose]
	if !chosen {
		return pol, nil
	}
	pol.set = set
	pol.rule = set.Rule
	if len(set.Pairs) > 0 {
		in := slices.DeleteFunc(slices.Clone(*eligible), func(m Model) bool { return m.Stale || !set.Has(m.Runtime, modelID(m)) })
		switch {
		case len(in) > 0:
			*eligible, pol.restricted = in, true
		case set.Fallback == agentpurposes.FallbackAny:
			pol.notes = append(pol.notes, "purpose "+st.Purpose+": none of the models the operator chose can run this worker; the operator allowed any validated model")
		default:
			return nil, fmt.Errorf("purpose %s: none of the models the operator chose (%s) is validated and able to run a %s worker; ask the operator to re-validate them or change the set in Settings", st.Purpose, pairKeys(set.Pairs), st.Access)
		}
	}
	if set.DifferentFromAuthor {
		pol.author = req.AuthorModel
		if pol.author == "" && s.Lead.Runtime != "" {
			pol.author = s.Lead.Key()
		}
		if pol.author != "" {
			others := slices.DeleteFunc(slices.Clone(*eligible), func(m Model) bool { return m.Key() == pol.author })
			if len(others) > 0 {
				*eligible = others
				pol.notes = append(pol.notes, "purpose "+st.Purpose+": a different model than the author "+pol.author)
			} else {
				pol.notes = append(pol.notes, "purpose "+st.Purpose+": only one validated model can run this, so it is the author's own "+pol.author+" (different-from-author fell back)")
				pol.author = ""
			}
		}
	}
	return pol, nil
}

// checkNamed refuses a model the lead named when the operator's setting
// leaves it out.
func (p *purposePolicy) checkNamed(m Model, eligible []Model) error {
	if p.restricted && !p.set.Has(m.Runtime, modelID(m)) {
		return fmt.Errorf("%s is not one of the models the operator allows for %s; use one of: %s", m.Key(), p.id, keys(eligible))
	}
	if p.restricted && m.Stale {
		return fmt.Errorf("%s is stale (not validated recently); the operator's set for %s allows: %s", m.Key(), p.id, keys(eligible))
	}
	if p.author != "" && m.Key() == p.author {
		return fmt.Errorf("%s wrote the work this worker checks, and the operator wants %s done by a different model; use one of: %s", m.Key(), p.id, keys(eligible))
	}
	return nil
}

// pick chooses by the operator's order or cheapest rule; any leaves the
// choice to the usual ranking (ok false). eligible is ranked, bad fits last.
func (p *purposePolicy) pick(eligible, bad []Model) (Model, bool) {
	if len(eligible) == 0 {
		return Model{}, false
	}
	switch p.rule {
	case agentpurposes.RuleOrder:
		for _, pair := range p.set.Pairs {
			if i := slices.IndexFunc(eligible, func(m Model) bool { return m.Key() == pair.Key() }); i >= 0 {
				return eligible[i], true
			}
		}
		return eligible[0], true
	case agentpurposes.RuleCheapest:
		fits := slices.DeleteFunc(slices.Clone(eligible), func(m Model) bool {
			return slices.ContainsFunc(bad, func(b Model) bool { return b.Key() == m.Key() })
		})
		if len(fits) == 0 {
			fits = eligible
		}
		best := fits[0]
		for _, m := range fits[1:] {
			if m.CostUSD < best.CostUSD || m.CostUSD == best.CostUSD && m.LatencyMs < best.LatencyMs {
				best = m
			}
		}
		return best, true
	}
	return Model{}, false
}

// permits is whether the operator's set for purpose lets m run its work:
// always when nothing is chosen, else only a validated pair of the set (or
// any validated model when the set has none available and the operator
// allowed the fallback).
func (s *Staffer) permits(purpose string, m Model) bool {
	set, chosen := s.Purposes[purpose]
	if !chosen || len(set.Pairs) == 0 {
		return true
	}
	if !m.Stale && set.Has(m.Runtime, modelID(m)) {
		return true
	}
	if set.Fallback != agentpurposes.FallbackAny {
		return false
	}
	return !slices.ContainsFunc(s.Roster, func(o Model) bool { return !o.Stale && set.Has(o.Runtime, modelID(o)) })
}

// modelID is the roster's id for a model: "default" for a runtime's own.
func modelID(m Model) string {
	if m.Model == "" {
		return "default"
	}
	return m.Model
}

func pairKeys(ps []agentpurposes.Pair) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Key()
	}
	return strings.Join(out, ", ")
}
