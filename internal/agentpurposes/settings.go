package agentpurposes

import (
	"fmt"
	"regexp"
	"strings"
)

// How the pairs of a purpose are used.
const (
	RuleAny      = "any"      // the default: code ranks the chosen pairs by fit, track record and cost
	RuleOrder    = "order"    // the first chosen pair that is available, in the operator's order
	RuleCheapest = "cheapest" // the cheapest chosen pair that meets the quality bar
)

// Rules lists the selection rules.
var Rules = []string{RuleAny, RuleOrder, RuleCheapest}

// MaxPairs bounds the pairs of one purpose.
const MaxPairs = 32

// Pair is one (runtime, model) the operator chose. Model "" is the runtime's default.
type Pair struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
}

// Key is the pair as the staffing names it: runtime/model, runtime/default for the default model.
func (p Pair) Key() string {
	if p.Model == "" {
		return p.Runtime + "/default"
	}
	return p.Runtime + "/" + p.Model
}

var (
	runtimeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	modelRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)
	effortRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

// normModel makes "default" and "" the same model.
func normModel(m string) string {
	m = strings.TrimSpace(m)
	if m == "default" {
		return ""
	}
	return m
}

// ParsePair reads "runtime/model", "runtime" or "runtime/" (the runtime's default model). The model
// may itself hold slashes ("openrouter/vendor/model").
func ParsePair(s string) (Pair, error) {
	rt, model, _ := strings.Cut(strings.TrimSpace(s), "/")
	p := Pair{Runtime: strings.TrimSpace(rt), Model: normModel(model)}
	if err := p.validate(); err != nil {
		return Pair{}, fmt.Errorf("%q: %w", s, err)
	}
	return p, nil
}

func (p Pair) validate() error {
	if !runtimeRE.MatchString(p.Runtime) {
		return fmt.Errorf("a pair is runtime/model, as in claude/opus (got runtime %q)", p.Runtime)
	}
	if p.Model != "" && (!modelRE.MatchString(p.Model) || strings.Contains(p.Model, "..")) {
		return fmt.Errorf("not a model id: %q", p.Model)
	}
	return nil
}

// Settings is what the operator set for one purpose.
type Settings struct {
	// Pairs are the chosen (runtime, model) pairs; empty means every validated pair.
	Pairs []Pair `json:"pairs"`
	// Rule is how the pairs are used; "" is RuleAny.
	Rule string `json:"rule,omitempty"`
	// Effort is a reasoning effort level to use when the chosen model offers it; "" is automatic.
	Effort string `json:"effort,omitempty"`
	// DifferentFromAuthor: never the model that wrote the work being reviewed, verified or judged.
	DifferentFromAuthor bool `json:"different_from_author,omitempty"`
	// BudgetSharePct is the share of the budget this purpose may use, 0 for none. Stored; not enforced yet.
	BudgetSharePct int `json:"budget_share_pct,omitempty"`
	// Fallback lets a spawn use any validated pair when none of the chosen ones is available.
	Fallback bool `json:"fallback,omitempty"`
}

// EffectiveRule is Rule, with "" read as RuleAny.
func (s Settings) EffectiveRule() string {
	if s.Rule == "" {
		return RuleAny
	}
	return s.Rule
}

// IsZero reports whether nothing is set: the purpose is back at its defaults.
func (s Settings) IsZero() bool {
	return len(s.Pairs) == 0 && s.Rule == "" && s.Effort == "" && !s.DifferentFromAuthor && s.BudgetSharePct == 0 && !s.Fallback
}

// Restricts reports whether the operator chose any pair: only then is a spawn limited to them.
func (s Settings) Restricts() bool { return len(s.Pairs) > 0 }

// Index is the position of the pair among the chosen ones, -1 when it is not chosen.
func (s Settings) Index(runtime, model string) int {
	model = normModel(model)
	for i, p := range s.Pairs {
		if p.Runtime == runtime && p.Model == model {
			return i
		}
	}
	return -1
}

// Contains reports whether the operator chose the pair.
func (s Settings) Contains(runtime, model string) bool { return s.Index(runtime, model) >= 0 }

// Normalize returns s with default models spelled "" and the strings trimmed.
func (s Settings) Normalize() Settings {
	out := s
	out.Pairs = make([]Pair, len(s.Pairs))
	for i, p := range s.Pairs {
		out.Pairs[i] = Pair{Runtime: strings.TrimSpace(p.Runtime), Model: normModel(p.Model)}
	}
	out.Rule = strings.TrimSpace(s.Rule)
	out.Effort = strings.TrimSpace(s.Effort)
	return out
}

// Validate checks s as the operator set it.
func (s Settings) Validate() error {
	s = s.Normalize()
	if len(s.Pairs) > MaxPairs {
		return fmt.Errorf("at most %d pairs per purpose", MaxPairs)
	}
	seen := map[string]bool{}
	for _, p := range s.Pairs {
		if err := p.validate(); err != nil {
			return err
		}
		if seen[p.Key()] {
			return fmt.Errorf("%s is chosen twice", p.Key())
		}
		seen[p.Key()] = true
	}
	if s.Rule != "" && s.Rule != RuleAny && s.Rule != RuleOrder && s.Rule != RuleCheapest {
		return fmt.Errorf("the rule is any, order or cheapest (got %q)", s.Rule)
	}
	if s.Effort != "" && !effortRE.MatchString(s.Effort) {
		return fmt.Errorf("not an effort level: %q", s.Effort)
	}
	if s.BudgetSharePct < 0 || s.BudgetSharePct > 100 {
		return fmt.Errorf("the budget share is 0 to 100 percent (got %d)", s.BudgetSharePct)
	}
	return nil
}

// Set is the settings of every purpose the operator set, by purpose id.
type Set map[string]Settings
