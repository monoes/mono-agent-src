package extension

import (
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Which profiles a paired browser's token may claim.
//
// A browser reports the profiles it runs in its auth frame, in binding frames
// and (from the bridge) in set_binding commands. Without a limit, any holder of
// its token can claim up to MaxProfilesPerBrowser profiles and receive their
// commands and session data. A PairedBrowser can therefore be Restricted to an
// allowed set; a claim naming any profile outside it is refused. Only the owner
// changes the set (`extension browsers allow`, operator-only).
//
// Restricted with an empty set means the browser may run no profile of its own
// (it serves the default browser). A record without Restricted, which is every
// record written before this limit existed, and the shared extension.token,
// are unrestricted: nothing about them changes until the owner restricts them.

// pairedBrowserByID is the paired entry with this exact id.
func pairedBrowserByID(id string) (PairedBrowser, bool) {
	if id == "" {
		return PairedBrowser{}, false
	}
	unlock, err := lockState()
	if err != nil {
		return PairedBrowser{}, false
	}
	defer unlock()
	list, _ := readBrowsersLocked()
	for _, e := range list {
		if e.ID == id {
			return e, true
		}
	}
	return PairedBrowser{}, false
}

// Outside returns the profiles of want that this browser may not claim, in order.
func (b PairedBrowser) Outside(want []string) []string {
	if !b.Restricted {
		return nil
	}
	var out []string
	for _, p := range want {
		if !containsString(b.AllowedProfiles, p) {
			out = append(out, p)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// SetAllowedProfiles restricts (or, with restricted false, unrestricts) the
// paired browser named by idOrPrefix (4+ characters). Ids profiledir would
// refuse are an error, not silently dropped: the owner gets what they typed
// or nothing.
func SetAllowedProfiles(idOrPrefix string, profiles []string, restricted bool) (PairedBrowser, error) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if len(idOrPrefix) < 4 {
		return PairedBrowser{}, fmt.Errorf("%w: give at least 4 characters of the id", ErrNoSuchBrowser)
	}
	clean := cleanProfiles(profiles)
	if len(clean) != len(dedupe(profiles)) {
		return PairedBrowser{}, fmt.Errorf("not a valid profile id in %q", profiles)
	}
	unlock, err := lockState()
	if err != nil {
		return PairedBrowser{}, err
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		return PairedBrowser{}, err
	}
	hit := -1
	for i, b := range list {
		if b.ID == idOrPrefix {
			hit = i
			break
		}
		if strings.HasPrefix(b.ID, idOrPrefix) {
			if hit != -1 {
				return PairedBrowser{}, fmt.Errorf("%w: %q matches more than one browser", ErrNoSuchBrowser, idOrPrefix)
			}
			hit = i
		}
	}
	if hit == -1 {
		return PairedBrowser{}, fmt.Errorf("%w: %q", ErrNoSuchBrowser, idOrPrefix)
	}
	list[hit].Restricted = restricted
	list[hit].AllowedProfiles = nil
	if restricted {
		list[hit].AllowedProfiles = clean
	}
	if err := writeBrowsersLocked(list); err != nil {
		return PairedBrowser{}, err
	}
	return list[hit], nil
}

// dedupe is in without blanks or repeats (what cleanProfiles keeps of valid input).
func dedupe(in []string) []string {
	var out []string
	for _, p := range in {
		if p = strings.TrimSpace(p); p != "" && !containsString(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// outsideAllowed is the profiles of want that the per-browser token behind c
// may not claim. A socket on the shared extension.token has no limit. When the
// token's record cannot be found (revoked, or the state unreadable) every
// profile counts as outside: the check fails closed.
func (c *extConn) outsideAllowed(want []string) []string {
	if c.tokenID == "" || len(want) == 0 {
		return nil
	}
	b, ok := pairedBrowserByID(c.tokenID)
	if !ok {
		return want
	}
	return b.Outside(want)
}

// setBindingNames is the profiles a set_binding command names.
func setBindingNames(p map[string]interface{}) []string {
	if list, ok := wireStrings(p["profiles"]); ok {
		return cleanProfiles(list)
	}
	if one, ok := p["profile"].(string); ok {
		return cleanProfiles([]string{one})
	}
	return nil
}

// closeUnauthorized closes c's socket with the 4401 code and a reason.
func (c *extConn) closeUnauthorized(reason string) {
	_ = c.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(unauthorizedCloseCode, reason), time.Now().Add(time.Second))
	_ = c.ws.Close()
}
