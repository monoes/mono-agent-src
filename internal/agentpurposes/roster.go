package agentpurposes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/agentroster"
)

// RosterPair is one pair of the validated roster with its status.
type RosterPair struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Status  string `json:"status"`
}

// statusOf maps a roster state onto a pair status.
func statusOf(state string) string {
	switch state {
	case agentroster.StateReady:
		return StatusOK
	case agentroster.StateStale:
		return StatusStale
	case agentroster.StateFailed:
		return StatusFailed
	}
	return StatusUnvalidated
}

// FromRoster flattens the roster into pairs, sorted by runtime and model.
func FromRoster(roster []agentroster.RuntimeRoster) []RosterPair {
	out := []RosterPair{}
	for _, rr := range roster {
		for _, e := range rr.Models {
			out = append(out, RosterPair{Runtime: rr.Runtime, Model: e.Model, Status: statusOf(e.State)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Runtime != out[j].Runtime {
			return out[i].Runtime < out[j].Runtime
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// StatusIn is the status of a pair in the roster; a pair the roster does
// not hold is unvalidated.
func StatusIn(roster []RosterPair, p Pair) string {
	for _, r := range roster {
		if r.Runtime == p.Runtime && r.Model == p.Model {
			return r.Status
		}
	}
	return StatusUnvalidated
}

// CheckValidated refuses a setting that names a pair that is not validated
// (status ok): only a validated pair may be chosen.
func CheckValidated(s Setting, roster []RosterPair) error {
	var bad []string
	for _, p := range s.Pairs {
		if st := StatusIn(roster, p); st != StatusOK {
			bad = append(bad, fmt.Sprintf("%s (%s)", p.Key(), st))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("only a validated model can be chosen; not validated: %s; run `monoagentcli agent validate` first", strings.Join(bad, ", "))
	}
	return nil
}
