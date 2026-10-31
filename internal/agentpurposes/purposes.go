// Package agentpurposes is the operator's choice of which models an agent
// purpose may use (monoes/mono-agent-src#1149). A purpose names a kind of
// work (coding, review, research, ...); the operator may set, for each, the
// (runtime, model) pairs of the validated roster it is limited to, and a few
// rules for choosing among them. A parent agent that spawns a sub-agent
// (internal/dynorg) draws its model from that set only, in code.
//
// The settings are profile-scoped rows of the configs table, written only
// through the storage layer's guarded writer (Store.Save), under a reserved
// prefix (operator.ReservedConfigPrefixes) that no agent may write.
package agentpurposes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/agentroster"
)

// Selection rules.
const (
	RuleAny      = "any"      // the ranking of fit, track record and cost, inside the set
	RuleOrder    = "order"    // the first available pair in the stated order
	RuleCheapest = "cheapest" // the cheapest pair that meets the quality bar
)

// Fallbacks when no pair of the set is available.
const (
	FallbackNone = "none" // the spawn fails with a reason
	FallbackAny  = "any"  // every validated model may be used
)

// Statuses of a pair, from the validated roster.
const (
	StatusOK          = "ok"
	StatusStale       = "stale"
	StatusFailed      = "failed"
	StatusUnvalidated = "unvalidated"
)

// Pair is one runtime and model, as the roster names them (the model "default"
// for a runtime that cannot list its models).
type Pair struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
}

// Key names a pair "runtime/model", as dynorg.Model.Key does.
func (p Pair) Key() string { return p.Runtime + "/" + p.Model }

// Setting is the operator's choice for one purpose.
type Setting struct {
	Pairs               []Pair `json:"pairs"`
	Rule                string `json:"rule"`
	Effort              string `json:"effort,omitempty"`
	DifferentFromAuthor bool   `json:"differentFromAuthor"`
	Fallback            string `json:"fallback"`
	BudgetSharePct      int    `json:"budgetSharePct"`
}

// Default is a setting that chooses nothing: every validated pair is used.
func Default() Setting {
	return Setting{Pairs: []Pair{}, Rule: RuleAny, Fallback: FallbackNone}
}

// IsDefault reports whether nothing is chosen, so every validated pair is
// used.
func (s Setting) IsDefault() bool { return len(s.Pairs) == 0 }

var effortToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// Validate checks the shape of a setting (not the roster).
func (s Setting) Validate() error {
	switch s.Rule {
	case RuleAny, RuleOrder, RuleCheapest:
	default:
		return fmt.Errorf("rule %q is not one of any, order, cheapest", s.Rule)
	}
	switch s.Fallback {
	case FallbackNone, FallbackAny:
	default:
		return fmt.Errorf("fallback %q is not one of any, none", s.Fallback)
	}
	if s.Effort != "" && !effortToken.MatchString(s.Effort) {
		return fmt.Errorf("effort %q is not an effort level name", s.Effort)
	}
	if s.BudgetSharePct < 0 || s.BudgetSharePct > 100 {
		return fmt.Errorf("budget share %d is not a percentage from 0 to 100", s.BudgetSharePct)
	}
	seen := map[string]bool{}
	for _, p := range s.Pairs {
		if p.Runtime == "" || p.Model == "" || strings.ContainsAny(p.Runtime, "/: \t") {
			return fmt.Errorf("pair %q/%q needs a runtime and a model", p.Runtime, p.Model)
		}
		if seen[p.Key()] {
			return fmt.Errorf("pair %s is listed twice", p.Key())
		}
		seen[p.Key()] = true
	}
	return nil
}

// Has reports whether the setting names the pair.
func (s Setting) Has(runtime, model string) bool {
	if model == "" {
		model = agentroster.DefaultModel
	}
	for _, p := range s.Pairs {
		if p.Runtime == runtime && p.Model == model {
			return true
		}
	}
	return false
}

// stored is the JSON of a configs row.
type stored struct {
	V int `json:"v"`
	Setting
}

// Encode is the value of the configs row.
func Encode(s Setting) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Pairs == nil {
		s.Pairs = []Pair{}
	}
	b, err := json.Marshal(stored{V: 1, Setting: s})
	return string(b), err
}

// Decode reads a configs row strictly: an unknown field, a wrong version or
// a value that fails Validate is an error, and the caller treats the row as
// the default.
func Decode(raw string) (Setting, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var st stored
	if err := dec.Decode(&st); err != nil {
		return Default(), fmt.Errorf("unreadable: %v", err)
	}
	if dec.More() {
		return Default(), fmt.Errorf("unreadable: trailing data")
	}
	if st.V != 1 {
		return Default(), fmt.Errorf("unreadable: version %d", st.V)
	}
	if err := st.Setting.Validate(); err != nil {
		return Default(), fmt.Errorf("invalid: %v", err)
	}
	if st.Pairs == nil {
		st.Pairs = []Pair{}
	}
	return st.Setting, nil
}
