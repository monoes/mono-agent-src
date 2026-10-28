package extension

import (
	"fmt"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// A browser runs a SET of monoagent profiles (any number may share it).
//
// On the wire an extension >= 1.12.0 reports `profiles` (the set) and also
// `profile` (the first id) so a bridge from before sets still routes. An
// older extension reports only `profile`, which is read as a one-element
// set. An empty set means the browser is unbound (a default browser).

// MaxProfilesPerBrowser bounds a browser's set. Keep identical to
// MAX_PROFILES in chrome-extension/browser_binding.js.
const MaxProfilesPerBrowser = 64

// MaxProfileIDLen bounds one profile id. Keep identical to MAX_PROFILE_ID in
// chrome-extension/browser_binding.js.
const MaxProfileIDLen = 128

// cleanProfiles drops ids profiledir would refuse (an id becomes a
// directory name further down), blanks and duplicates, keeping order, and
// keeps at most MaxProfilesPerBrowser of them. Ids over MaxProfileIDLen
// bytes are dropped too.
func cleanProfiles(in []string) []string {
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > MaxProfileIDLen || !profiledir.ValidProfileID(p) || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
		if len(out) == MaxProfilesPerBrowser {
			break
		}
	}
	return out
}

// profilesFromWire is the set an auth/binding frame or a set_binding
// carries: profiles when the sender knows sets, else the single profile.
func profilesFromWire(profiles []string, profile string) []string {
	if len(profiles) > 0 {
		return cleanProfiles(profiles)
	}
	return cleanProfiles([]string{profile})
}

// ProfileSet is the profiles this browser runs. It reads profile_ids when
// the bridge reported them and falls back to the single profile of an older
// bridge, so a newer CLI can talk to an older bridge.
func (i ConnInfo) ProfileSet() []string {
	if len(i.ProfileIDs) > 0 {
		return i.ProfileIDs
	}
	if i.Profile != "" {
		return []string{i.Profile}
	}
	return nil
}

// SupportsProfileSets reports whether an extension of this version can run
// more than one profile (1.12.0 and later). An extension that cannot keeps
// only the first profile it is sent.
func SupportsProfileSets(version string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 1 || (major == 1 && minor >= 12)
}

// SetKnown reports whether ProfileSet is the browser's whole set. A bridge
// that predates sets reports only the first profile of an extension that
// has several, so a client must not write back a set computed from it.
func (i ConnInfo) SetKnown() bool {
	return len(i.ProfileIDs) > 0 || i.Profile == "" || !SupportsProfileSets(i.Version)
}

// RunsProfile reports whether p is in this browser's set.
func (i ConnInfo) RunsProfile(p string) bool {
	return p != "" && slices.Contains(i.ProfileSet(), p)
}
