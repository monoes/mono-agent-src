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
	label      string // the purposes whose sets limit the choice, for messages
	set        agentpurposes.Setting
	rule       string
	limited    bool   // some set limits the choice: a stale model is never staffed
	restricted bool   // the candidates are limited to the set's pairs
	author     string // a model the worker must differ from, "" = none
	notes      []string
}

// staffPurpose settles the purposes whose model sets limit the worker. They
// come from the ROLE's category (agentpurposes.ScopeFor), never from the lead:
// a built-in role is limited by its category's default purpose, and a purpose
// the lead names can only narrow it (it must be one the category allows, and
// both sets then apply). Any other role (library, monomind, a file in the chat
// folder, which a worker can write) has no trusted category: whatever it
// claims, it is limited by the sets of every purpose the operator restricted,
// the most restrictive default, and a purpose named for it is ignored.
func (s *Staffer) staffPurpose(req SpawnRequest, st *Staff) error {
	named := strings.TrimSpace(req.Purpose)
	if named != "" {
		if _, ok := agentpurposes.Lookup(named); !ok {
			return fmt.Errorf("unknown purpose %q; leave purpose empty to derive it from the role, or use one of: %s", named, strings.Join(agentpurposes.IDs(), ", "))
		}
	}
	sc, mapped := agentpurposes.ScopeFor(st.Category)
	if !st.Builtin {
		mapped, named = false, ""
	}
	if named != "" && mapped && !sc.Allows(named) {
		return fmt.Errorf("purpose %q does not fit a %s role: a purpose can only narrow the role's own (%s); use one of: %s, or leave it out", named, st.Category, sc.Default, strings.Join(sc.Allowed, ", "))
	}
	st.Purpose, st.sides = named, nil
	if mapped {
		st.Purpose = sc.Default
		if named != "" && named != sc.Default {
			st.Purpose, st.sides = named, []string{named}
		}
		st.sides = append(st.sides, sc.Default)
		for _, id := range st.also {
			if !slices.Contains(st.sides, id) {
				st.sides = append(st.sides, id)
			}
		}
		return nil
	}
	if named != "" {
		st.sides = []string{named}
	}
	for _, id := range agentpurposes.IDs() {
		if id != named {
			st.sides = append(st.sides, id)
		}
	}
	return nil
}

// settings are the operator's sets for this spawn decision, read fresh each
// time so a change made mid-turn applies to the next spawn. Any error fails
// the spawn: an unknown restriction is never read as none.
func (s *Staffer) settings() (map[string]agentpurposes.Setting, error) {
	if s.PurposesFn == nil {
		return s.Purposes, nil
	}
	all, err := s.PurposesFn()
	if err != nil {
		return nil, fmt.Errorf("no worker starts: the operator's model sets can't be read: %w", err)
	}
	return all, nil
}

// limitToPurpose applies the operator's sets for the worker's purposes to the
// candidates: only pairs of every restricted set (their intersection) that
// are validated (not stale) stay, a model that wrote the work is left out
// when a set asks for another, and a set with nothing available fails the
// spawn unless the operator allowed the fallback. The check is in code:
// nothing the lead says widens it.
func (s *Staffer) limitToPurpose(req SpawnRequest, st *Staff, eligible *[]Model) (*purposePolicy, error) {
	pol := &purposePolicy{id: st.Purpose, label: st.Purpose, rule: agentpurposes.RuleAny}
	all, err := s.settings()
	if err != nil {
		return nil, err
	}
	st.loaded = all
	if p, ok := all[st.Purpose]; ok && st.Purpose != "" {
		pol.set, pol.rule = p, p.Rule
	}
	var limits []string
	var inter []agentpurposes.Pair
	fallbackAny, diff := true, false
	for _, id := range st.sides {
		set, ok := all[id]
		if !ok {
			continue
		}
		diff = diff || set.DifferentFromAuthor
		if len(set.Pairs) == 0 {
			continue
		}
		if len(limits) == 0 {
			inter = slices.Clone(set.Pairs)
		} else {
			inter = slices.DeleteFunc(inter, func(p agentpurposes.Pair) bool { return !set.Has(p.Runtime, p.Model) })
		}
		limits = append(limits, id)
		fallbackAny = fallbackAny && set.Fallback == agentpurposes.FallbackAny
	}
	if len(limits) > 0 {
		pol.label = strings.Join(limits, " and ")
		if len(limits) == 1 {
			pol.label = limits[0]
		}
		if len(inter) == 0 {
			return nil, fmt.Errorf("purpose %s: the models the operator chose for these have none in common, so no model may run this worker; ask the operator to change the sets in Settings", pol.label)
		}
		pol.set.Pairs, pol.limited = inter, true
		in := slices.DeleteFunc(slices.Clone(*eligible), func(m Model) bool { return m.Stale || !pol.set.Has(m.Runtime, modelID(m)) })
		switch {
		case len(in) > 0:
			*eligible, pol.restricted = in, true
		case fallbackAny:
			if *eligible = slices.DeleteFunc(*eligible, func(m Model) bool { return m.Stale }); len(*eligible) == 0 {
				return nil, fmt.Errorf("purpose %s: none of the models the operator chose is validated, and no other validated model can run a %s worker; ask the operator to re-validate models", pol.label, st.Access)
			}
			pol.notes = append(pol.notes, "purpose "+pol.label+": none of the models the operator chose can run this worker; the operator allowed any validated model")
		default:
			return nil, fmt.Errorf("purpose %s: none of the models the operator chose (%s) is validated and able to run a %s worker; ask the operator to re-validate them or change the set in Settings", pol.label, pairKeys(inter), st.Access)
		}
	}
	if diff {
		pol.author = req.AuthorModel
		if pol.author == "" && s.Lead.Runtime != "" {
			pol.author = s.Lead.Key()
		}
		if pol.author != "" {
			others := slices.DeleteFunc(slices.Clone(*eligible), func(m Model) bool { return m.Key() == pol.author })
			if len(others) > 0 {
				*eligible = others
				pol.notes = append(pol.notes, "purpose "+pol.label+": a different model than the author "+pol.author)
			} else {
				pol.notes = append(pol.notes, "purpose "+pol.label+": only one validated model can run this, so it is the author's own "+pol.author+" (different-from-author fell back)")
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
		return fmt.Errorf("%s is not one of the models the operator allows for %s; use one of: %s", m.Key(), p.label, keys(eligible))
	}
	if p.limited && m.Stale {
		return fmt.Errorf("%s is stale (not validated recently); the operator's set for %s allows: %s", m.Key(), p.label, keys(eligible))
	}
	if p.author != "" && m.Key() == p.author {
		return fmt.Errorf("%s wrote the work this worker checks, and the operator wants %s done by a different model; use one of: %s", m.Key(), p.label, keys(eligible))
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

// permits is whether the operator's set for purpose (all: the sets read for
// this decision) lets m run its work: always when nothing is chosen, else only
// a validated pair of the set (or any validated model when the set has none
// available and the operator allowed the fallback).
func (s *Staffer) permits(all map[string]agentpurposes.Setting, purpose string, m Model) bool {
	set, chosen := all[purpose]
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
