package coderhost

import (
	"strings"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// ValidHostPursuitInfo says whether the pursuit facts the host published can be real: a loop that is on has a wake-up cap the host
// would have accepted (validWakeCap) and a judge pin that is a printable runtime/model name. host.json is display-only and any
// process running as the user can write it, so a value that fails here is never shown to the person as a fact.
func ValidHostPursuitInfo(h PursuitHostInfo) bool {
	for _, t := range []string{h.VerifierPin, h.VerifierReason, h.Confinement} { // shown to the person: bounded and plain
		if len(t) > 600 {
			return false
		}
		for _, r := range t {
			if missionpursuit.HiddenRune(r) {
				return false
			}
		}
	}
	if !h.Enabled {
		return true
	}
	if !validWakeCap(h.WakeCapUSD) {
		return false
	}
	p := h.Pin
	if len(p) < 3 || len(p) > 200 || !strings.Contains(p, "/") || p != strings.TrimSpace(p) {
		return false
	}
	for _, r := range p {
		if r == ' ' || missionpursuit.HiddenRune(r) {
			return false
		}
	}
	return true
}
