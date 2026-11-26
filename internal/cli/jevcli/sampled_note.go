package jevcli

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// sampledNote says what a threshold means for p when it is known to sample its
// probabilities, "" otherwise. The surfaces it names are the ones that lower a
// threshold (jevconf.SampledLoweredSurfaces).
func sampledNote(p jevconf.Provider) string {
	if p.Caps.Probability != jev.ProbSampled {
		return ""
	}
	return sampledIntro(p) + fmt.Sprintf(" Only %s lower a higher threshold to that, so it passes only when all 5 agree; "+
		"every other surface, the ai.choose, ai.decide, browser.jev and applications.evaluate nodes and the org decider "+
		"keep the configured threshold.", loweredSurfaceList())
}

// surfaceSampledNote is sampledNote as `jev enable s` prints it.
func surfaceSampledNote(p jevconf.Provider, s jevconf.Surface) string {
	if p.Caps.Probability != jev.ProbSampled {
		return ""
	}
	rule := "keeps the configured threshold: a threshold above that never passes there."
	if jevconf.LowersSampled(s) {
		rule = "lowers a higher threshold to that, so it passes only when all 5 agree."
	}
	return sampledIntro(p) + fmt.Sprintf(" Surface %s %s Only %s lower a threshold.", s, rule, loweredSurfaceList())
}

func sampledIntro(p jevconf.Provider) string {
	return fmt.Sprintf("Provider %s estimates probabilities from 5 answers, which read at most 6/(5+options) "+
		"(0.86 on two options, 0.75 on three, less with more).", p.Name)
}

// loweredSurfaceList is the surfaces that lower a threshold, as "a, b and c".
func loweredSurfaceList() string {
	var names []string
	for _, s := range jevconf.SampledLoweredSurfaces() {
		names = append(names, string(s))
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// sampledNotes is sampledNote of each provider a surface of st uses, once each.
func sampledNotes(db *sql.DB, st jevStatus) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range st.Surfaces {
		name := st.Provider
		if s.ProviderPinned {
			name = s.Provider
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if p, ok, err := jevconf.FindProvider(db, name); ok && err == nil {
			if note := sampledNote(p); note != "" {
				out = append(out, note)
			}
		}
	}
	return out
}
