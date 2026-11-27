package dynorg

import (
	"slices"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

// fitSlack is how far below the best fit a model may be and still count
// as fitting the category equally well: within it, price decides.
const fitSlack = 0.1

// heavyCategories are the kinds of work that need the strongest ready
// model; everything else runs on the cheapest model that fits.
var heavyCategories = append([]string{"engineering", "development", "architecture", "security", "debug", "devops", "backend", "frontend", "implement"}, leadingCategories...)

// leadingCategories are the kinds of work that direct other work: they take
// the strongest ready model and never a small one.
var leadingCategories = []string{"planning", "strategy", "orchestrating", "judging"}

// isLeading reports whether a worker's category plans or leads.
func isLeading(category string) bool {
	c := strings.ToLower(category)
	return slices.ContainsFunc(leadingCategories, func(l string) bool { return strings.Contains(c, l) })
}

// withoutSmall drops the small models from a planner's or leader's choices,
// unless nothing else can do the work.
func withoutSmall(eligible []Model, category string) []Model {
	if !isLeading(category) {
		return eligible
	}
	big := slices.DeleteFunc(slices.Clone(eligible), func(m Model) bool { return monomind.IsSmallModel(m.Model) })
	if len(big) == 0 {
		return eligible
	}
	return big
}

// isHeavy reports whether a worker's category needs the strongest model.
// A role with no category counts as heavy only when it edits files.
func isHeavy(category, access string) bool {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" || c == agentroster.CategoryGeneral {
		return writes(access)
	}
	return slices.ContainsFunc(heavyCategories, func(h string) bool { return strings.Contains(c, h) })
}

// fitScore is a model's known success rate for the category, else the
// prior every model starts from, so unknown models are neither favored
// nor punished.
func (s *Staffer) fitScore(m Model, category string) float64 {
	if r, ok := s.trackRecord(m, category); ok {
		return r.Rate
	}
	return agentroster.QualityPriorRate
}

// ruleModel is the model choice without Jev, with no bias to the lead's
// own model: research goes to the cheapest, fastest ready model that runs
// read-only (--access read or a read-only sandbox; one that doesn't must
// hold the write lease), else the cheapest. Other work goes to the best
// fit for the category; among models that fit about equally, heavy work
// takes the priciest (a stand-in for the strongest) and the rest the
// cheapest. A planner or leader takes the priciest read-only one too.
func ruleModel(eligible []Model, access string, heavy, leading bool, fit func(Model) float64) Model {
	sorted := slices.Clone(eligible)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].CostUSD != sorted[j].CostUSD {
			return sorted[i].CostUSD < sorted[j].CostUSD
		}
		return sorted[i].LatencyMs < sorted[j].LatencyMs
	})
	if access == ProfileResearch {
		readers := slices.DeleteFunc(slices.Clone(sorted), func(m Model) bool { return !m.Read && !m.ReadOnlySandbox })
		if len(readers) == 0 {
			readers = sorted
		}
		if leading {
			return readers[len(readers)-1]
		}
		return readers[0]
	}
	best := 0.0
	for _, m := range sorted {
		best = max(best, fit(m))
	}
	near := sorted[:0:0]
	for _, m := range sorted {
		if fit(m) >= best-fitSlack {
			near = append(near, m)
		}
	}
	if heavy {
		return near[len(near)-1]
	}
	return near[0]
}

// costTier is a relative price hint for org_roster, from the validation
// turn's cost: the cheapest model is "cheap", the priciest "strong".
func costTier(m Model, all []Model) string {
	lo, hi := m.CostUSD, m.CostUSD
	for _, o := range all {
		lo, hi = min(lo, o.CostUSD), max(hi, o.CostUSD)
	}
	switch {
	case hi == lo:
		return "standard"
	case m.CostUSD == lo:
		return "cheap"
	case m.CostUSD == hi:
		return "strong"
	}
	return "standard"
}

// ModelHint is org_roster's cost and quality hint for one ready model.
type ModelHint struct {
	Model   string             `json:"model"`
	Tier    string             `json:"tier"` // cheap, standard or strong (by test-turn cost)
	Quality map[string]float64 `json:"quality_by_category,omitempty"`
	Use     string             `json:"use"`
}

// ModelHints tells the lead which ready model suits which work.
func (s *Staffer) ModelHints() []ModelHint {
	out := make([]ModelHint, 0, len(s.Roster))
	for _, m := range s.Roster {
		h := ModelHint{Model: m.Key(), Tier: costTier(m, s.Roster)}
		switch h.Tier {
		case "cheap":
			h.Use = "research, docs, review, tasks, tests"
		case "strong":
			h.Use = "hard implementation, security, architecture"
		default:
			h.Use = "general work"
		}
		for _, r := range s.Quality.ForModel(m.Runtime, m.Model) {
			if r.Known {
				if h.Quality == nil {
					h.Quality = map[string]float64{}
				}
				h.Quality[r.Category] = round2(r.Rate)
			}
		}
		out = append(out, h)
	}
	return out
}
