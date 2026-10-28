package extension

import (
	"errors"
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

// policySet is the allowed-set records of the paired browsers behind a list
// of sockets, read once so one routing decision sees one state.
type policySet struct {
	byID   map[string]PairedBrowser
	failed bool // the state could not be read: limited sockets are not eligible
}

func pairedPolicies(conns []*extConn) policySet {
	p := policySet{byID: map[string]PairedBrowser{}}
	need := false
	for _, c := range conns {
		if c.tokenID != "" {
			need = true
		}
	}
	if !need {
		return p
	}
	unlock, err := lockState()
	if err != nil {
		p.failed = true
		return p
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		p.failed = true
		return p
	}
	for _, e := range list {
		p.byID[e.ID] = e
	}
	return p
}

// allows reports whether c may serve profile (empty: a command that names no
// profile). A shared-token socket serves anything; a per-browser one serves
// what its record allows, and nothing once the record is gone or unreadable.
func (p policySet) allows(c *extConn, profile string) bool {
	if c.tokenID == "" {
		return true
	}
	b, ok := p.byID[c.tokenID]
	if p.failed || !ok {
		return false
	}
	if !b.Restricted {
		return true
	}
	return profile != "" && containsString(b.AllowedProfiles, profile)
}

// whileAllowed runs fn only if every profile in want is allowed for c's token
// at this moment, with the state lock held so the owner's change cannot land
// between the check and fn. It reports whether fn ran. Lock order: bindMu,
// then the state lock, then c.mu / writeMu; nothing takes them the other way.
func (c *extConn) whileAllowed(want []string, fn func()) bool {
	if c.tokenID == "" || len(want) == 0 {
		fn()
		return true
	}
	unlock, err := lockState()
	if err != nil {
		return false
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		return false
	}
	for _, e := range list {
		if e.ID == c.tokenID {
			if len(e.Outside(want)) > 0 {
				return false
			}
			fn()
			return true
		}
	}
	return false
}

// ErrProfileNotAllowed: the browser's token is limited to profiles that do not include the one asked for.
var ErrProfileNotAllowed = errors.New("not allowed for this browser")

func errNotAllowed(out []string) error {
	first := ""
	if len(out) > 0 {
		first = out[0]
	}
	return fmt.Errorf("profile %q is %w: change what it may run with `monoagentcli extension browsers allow`", first, ErrProfileNotAllowed)
}

// whileServing runs fn only if c's token may serve profile at this moment
// (profile "" is a command that names none), with the state lock held so the
// owner's change cannot land between the check and fn. It reports whether fn
// ran. A socket on the shared token serves anything.
func (c *extConn) whileServing(profile string, fn func()) bool {
	if c.tokenID == "" {
		fn()
		return true
	}
	unlock, err := lockState()
	if err != nil {
		return false
	}
	defer unlock()
	list, err := readBrowsersLocked()
	if err != nil {
		return false
	}
	for _, e := range list {
		if e.ID == c.tokenID {
			if e.Restricted && (profile == "" || !containsString(e.AllowedProfiles, profile)) {
				return false
			}
			fn()
			return true
		}
	}
	return false
}

// writeBounded runs write, closing the socket if it has not finished after
// deliveryWriteTimeout: it is called with the state lock held, and a browser
// that stops reading must not stall every pairing and policy change.
func (c *extConn) writeBounded(write func() error) error {
	stuck := time.AfterFunc(deliveryWriteTimeout, func() { _ = c.ws.Close() })
	defer stuck.Stop()
	return write()
}

// deliveryWriteTimeout bounds a command write made under the state lock.
const deliveryWriteTimeout = 2 * time.Second

// beforeDeliver, set by tests only, runs between choosing a browser and delivering to it.
var beforeDeliver func()

// deliver writes cmd to c if c's token may serve profile right now.
func (s *Server) deliver(c *extConn, profile string, cmd *Command) error {
	if beforeDeliver != nil {
		beforeDeliver()
	}
	var werr error
	if !c.whileServing(profile, func() { werr = c.writeBounded(func() error { return s.writeCommandTo(c, cmd) }) }) {
		return errNotAllowed([]string{profile})
	}
	return werr
}
