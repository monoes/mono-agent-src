package dynorg

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// The ranking of candidate directions (docs/mastermind/plans/2026-10-28-mission-research-directions.md, section 4,
// phase EVALUATE). It is a pure function: the same input gives the same order on every run, with no model and no clock,
// so the lead cannot talk a direction up; it can only change the scores it states, and those are recorded.

// Criteria are scored 0 to 10. For success, higher is better; for cost, risk and effort, higher is worse and counts
// as 10 minus the score.
const (
	CritSuccess = "success"
	CritCost    = "cost"
	CritRisk    = "risk"
	CritEffort  = "effort"
)

// Criteria in the order they are listed everywhere.
var Criteria = []string{CritSuccess, CritCost, CritRisk, CritEffort}

// DefaultWeights are used when the lead states none: the success measure counts three times a cost.
var DefaultWeights = map[string]float64{CritSuccess: 3, CritCost: 1, CritRisk: 1, CritEffort: 1}

// Bounds on what one evaluation may carry.
const (
	MaxCandidates   = 20
	MaxEvidenceRefs = 10
	MaxTitleChars   = 120
	MaxThesisChars  = 1000
	MaxRefChars     = 200
	// DefaultDirections is K when the operator sets none; HardMaxDirections is the most the operator can set.
	DefaultDirections = 3
	HardMaxDirections = 6
	maxScore          = 10
	maxWeight         = 100
	tieEpsilon        = 1e-9
)

// DirCandidate is one direction the lead considers.
type DirCandidate struct {
	Title    string             `json:"title"`
	Thesis   string             `json:"thesis"`
	Evidence []string           `json:"evidence,omitempty"`
	Scores   map[string]float64 `json:"scores"`
	// MergeWith names another candidate this one should be folded into (mission_reevaluate only).
	MergeWith string `json:"merge_with,omitempty"`
}

// Ranked is a candidate with its place.
type Ranked struct {
	Rank   int     `json:"rank"`
	Title  string  `json:"title"`
	Score  float64 `json:"score"`
	Risk   float64 `json:"risk"`
	Cost   float64 `json:"cost"`
	Picked bool    `json:"picked"`
}

func validNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// ValidateCandidates checks the sizes, counts and numbers of an evaluation. Titles are unique (compared with the case
// and spacing folded), every candidate scores every criterion, and a score is a finite number from 0 to 10.
func ValidateCandidates(cs []DirCandidate) error {
	if len(cs) == 0 {
		return fmt.Errorf("name at least one candidate direction")
	}
	if len(cs) > MaxCandidates {
		return fmt.Errorf("at most %d candidate directions; narrow the list first", MaxCandidates)
	}
	seen := map[string]bool{}
	for i, c := range cs {
		key := titleKey(c.Title)
		switch {
		case key == "" || len([]rune(c.Title)) > MaxTitleChars:
			return fmt.Errorf("candidate %d needs a title of at most %d characters", i+1, MaxTitleChars)
		case seen[key]:
			return fmt.Errorf("candidate %q is listed twice", oneLine(c.Title))
		case len([]rune(c.Thesis)) > MaxThesisChars:
			return fmt.Errorf("candidate %q: the thesis is over %d characters", oneLine(c.Title), MaxThesisChars)
		case len(c.Evidence) > MaxEvidenceRefs:
			return fmt.Errorf("candidate %q: at most %d evidence references", oneLine(c.Title), MaxEvidenceRefs)
		}
		seen[key] = true
		for _, r := range c.Evidence {
			if len([]rune(r)) > MaxRefChars {
				return fmt.Errorf("candidate %q: an evidence reference is over %d characters", oneLine(c.Title), MaxRefChars)
			}
		}
		for k := range c.Scores {
			if !contains(Criteria, k) {
				return fmt.Errorf("candidate %q: %q is not a criterion (%s)", oneLine(c.Title), k, strings.Join(Criteria, ", "))
			}
		}
		for _, k := range Criteria {
			v, ok := c.Scores[k]
			if !ok || !validNumber(v) || v < 0 || v > maxScore {
				return fmt.Errorf("candidate %q: score %s must be a number from 0 to %d", oneLine(c.Title), k, maxScore)
			}
		}
	}
	return nil
}

// ValidateWeights checks the weights: each a finite number from 0 to 100, a known criterion, and not all zero. A nil
// map means the defaults.
func ValidateWeights(w map[string]float64) (map[string]float64, error) {
	if len(w) == 0 {
		return DefaultWeights, nil
	}
	sum := 0.0
	out := map[string]float64{}
	for _, k := range Criteria {
		v := w[k] // a criterion left out weighs nothing
		if !validNumber(v) || v < 0 || v > maxWeight {
			return nil, fmt.Errorf("weight %s must be a number from 0 to %d", k, maxWeight)
		}
		out[k] = v
		sum += v
	}
	for k := range w {
		if !contains(Criteria, k) {
			return nil, fmt.Errorf("%q is not a criterion (%s)", k, strings.Join(Criteria, ", "))
		}
	}
	if sum <= 0 {
		return nil, fmt.Errorf("at least one weight must be above 0")
	}
	return out, nil
}

// Rank orders validated candidates by weighted score, best first. A tie (within 1e-9) goes to the lower risk, then the
// lower cost, then the candidate stated first. The first k are marked picked.
func Rank(cs []DirCandidate, w map[string]float64, k int) []Ranked {
	sum := 0.0
	for _, c := range Criteria {
		sum += w[c]
	}
	out := make([]Ranked, len(cs))
	for i, c := range cs {
		s := w[CritSuccess]*c.Scores[CritSuccess] + w[CritCost]*(maxScore-c.Scores[CritCost]) +
			w[CritRisk]*(maxScore-c.Scores[CritRisk]) + w[CritEffort]*(maxScore-c.Scores[CritEffort])
		out[i] = Ranked{Title: c.Title, Score: s / sum, Risk: c.Scores[CritRisk], Cost: c.Scores[CritCost]}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if math.Abs(a.Score-b.Score) > tieEpsilon {
			return a.Score > b.Score
		}
		if a.Risk != b.Risk {
			return a.Risk < b.Risk
		}
		return a.Cost < b.Cost
	})
	for i := range out {
		out[i].Rank = i + 1
		out[i].Picked = i < k
	}
	return out
}

func titleKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
