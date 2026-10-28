package extension

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// One bridge, several browsers.
//
// Every browser profile with the extension installed runs its own copy of
// it: its own service worker, its own chrome.storage, its own socket to this
// server. The server used to keep exactly one of those sockets, and a second
// one knocked the first off. So two browser profiles could never be driven
// at once, and since a browser profile holds one cookie jar per site,
// neither could two accounts on the same site.
//
// Now each socket is kept under the instance id its extension generated
// once and reports in its auth frame, along with the monoagent profile the
// user bound that browser to. A command goes to one browser, chosen by a
// Target (see resolve). An extension too old to report an instance id is
// kept under legacyInstanceID. That is exactly the old single-socket
// behaviour, for everyone who has not updated the extension.

// legacyInstanceID keys the socket of an extension that sent no usable
// instance id. There is one such slot, so two old extensions still replace
// each other the way every extension used to.
const legacyInstanceID = "legacy"

// validInstanceID bounds what an extension may call itself. The id shows
// up in URLs, logs and CLI output, so it is held to a UUID-ish alphabet.
var validInstanceID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// maxLabelLen bounds the user-chosen browser label.
const maxLabelLen = 60

// Target names the browser a command is for. Instance wins when set. A
// Profile resolves to the browser bound to that monoagent profile. The zero
// Target is "the browser a caller that knows nothing about profiles would
// have reached before", i.e. the default browser.
type Target struct {
	Profile  string
	Instance string
}

// ConnInfo describes one connected browser.
type ConnInfo struct {
	Instance string `json:"instance"`
	// Profile is the first profile of ProfileIDs, kept for older CLIs.
	Profile string `json:"profile,omitempty"`
	// ProfileIDs is every profile this browser runs; empty = unbound.
	ProfileIDs  []string  `json:"profile_ids,omitempty"`
	Label       string    `json:"label,omitempty"`
	Version     string    `json:"version,omitempty"`
	Legacy      bool      `json:"legacy,omitempty"`
	ConnectedAt time.Time `json:"connectedAt"`
	// Conflict: another connected browser runs one of the same profiles.
	// Only the newest of them is used for that profile.
	Conflict bool `json:"conflict,omitempty"`
}

// ErrNoExtension means no browser is connected at all. The words are the
// ones every caller already knows from before there were several.
var ErrNoExtension = errors.New("no extension connected")

// ErrBrowserGone means a specific browser was asked for and is not
// connected, e.g. it was closed in the middle of a run.
var ErrBrowserGone = errors.New("that browser is not connected")

// NoBrowserError means browsers are connected, but none may run this
// profile: each one runs only other profiles, and there is no unbound
// (default) browser to fall back to.
type NoBrowserError struct {
	Profile string
	BoundTo []string // the profiles the connected browsers ARE bound to
}

func (e *NoBrowserError) Error() string {
	return fmt.Sprintf("no browser is set up for profile %q (the connected browsers run profiles %s) — "+
		"open the MonoAgent Bridge side panel in the browser this profile should use and tick it under "+
		"\"Automations in this browser\", or run `monoagentcli extension bind <browser> %s`",
		e.Profile, strings.Join(e.BoundTo, ", "), e.Profile)
}

// extConn is one browser's socket.
type extConn struct {
	id          string
	tokenID     string // the per-browser token this socket authenticated with, if any
	legacy      bool
	version     string
	connectedAt time.Time

	ws      *websocket.Conn
	writeMu sync.Mutex // gorilla/websocket forbids concurrent writers, per socket

	// bindMu serializes set_binding commands to this browser, from narrowing
	// the set to confirming or restoring it.
	bindMu sync.Mutex

	mu       sync.Mutex // guards profiles and label, which change while connected
	profiles []string
	label    string
}

func newExtConn(ws *websocket.Conn, auth authFrame, now time.Time) *extConn {
	c := &extConn{id: legacyInstanceID, legacy: true, ws: ws, connectedAt: now, tokenID: auth.tokenID}
	if validInstanceID.MatchString(auth.Instance) {
		c.id, c.legacy = auth.Instance, false
	}
	c.version = cleanLabel(auth.Version)
	c.setBinding(profilesFromWire(auth.Profiles, auth.Profile), auth.Label)
	return c
}

// setBinding records which profiles the browser says it runs. Ids that
// profiledir would refuse are dropped rather than trusted.
func (c *extConn) setBinding(profiles []string, label string) {
	profiles = cleanProfiles(profiles)
	c.mu.Lock()
	c.profiles = profiles
	c.label = cleanLabel(label)
	c.mu.Unlock()
}

func cleanLabel(s string) string {
	s = strings.Join(strings.Fields(SafeText(s)), " ")
	if r := []rune(s); len(r) > maxLabelLen {
		s = string(r[:maxLabelLen])
	}
	return s
}

// boundProfiles is a copy of the profiles this browser runs.
func (c *extConn) boundProfiles() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.profiles)
}

func (c *extConn) write(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

func (c *extConn) ping() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteMessage(websocket.PingMessage, nil)
}

func (c *extConn) info() ConnInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := ConnInfo{
		Instance: c.id, Label: c.label, Version: c.version,
		Legacy: c.legacy, ConnectedAt: c.connectedAt,
	}
	if len(c.profiles) > 0 {
		info.Profile = c.profiles[0]
		info.ProfileIDs = slices.Clone(c.profiles)
	}
	return info
}

// installConn makes c the socket for its browser and closes the one it
// replaces. That is routine: an MV3 service worker that respawns before its
// previous socket was reaped reconnects on top of itself many times a day.
// It refuses (false) when the socket it would replace authenticated with a
// per-browser token and c did not present that same token: only the browser
// itself, or the shared token's owner after revoking it, takes that slot.
func (s *Server) installConn(c *extConn) bool {
	s.connMu.Lock()
	if s.conns == nil {
		s.conns = make(map[string]*extConn)
	}
	old := s.conns[c.id]
	if old != nil && old.tokenID != "" && old.tokenID != c.tokenID {
		s.connMu.Unlock()
		return false
	}
	s.conns[c.id] = c
	s.connMu.Unlock()
	if old != nil {
		s.logger.Info().Str("instance", c.id).Msg("replacing the previous connection from this browser")
		_ = old.ws.Close()
	}
	return true
}

// removeConn forgets c, unless a newer socket from the same browser has
// already taken its slot.
func (s *Server) removeConn(c *extConn) {
	s.connMu.Lock()
	if s.conns[c.id] == c {
		delete(s.conns, c.id)
	}
	s.connMu.Unlock()
}

// connList is every connected browser, newest first.
func (s *Server) connList() []*extConn {
	s.connMu.Lock()
	out := make([]*extConn, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, c)
	}
	s.connMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].connectedAt.After(out[j].connectedAt) })
	return out
}

// resolve picks the browser a command for t goes to:
//
//   - an Instance: that browser, or ErrBrowserGone;
//   - a Profile: the newest browser whose set contains it (browsers may
//     share profiles; the newest wins); failing that, the newest unbound
//     browser (the "default browser" everyone had before binding existed);
//     never a browser that runs only OTHER profiles, because that browser
//     is logged into their accounts;
//   - neither: the newest unbound browser, else the newest browser, which
//     is what an older client that sends no target always got.
func (s *Server) resolve(t Target) (*extConn, error) {
	conns := s.connList()
	if len(conns) == 0 {
		return nil, ErrNoExtension
	}
	if t.Instance != "" {
		for _, c := range conns {
			if c.id == t.Instance {
				if t.Profile != "" && !pairedPolicies(conns).allows(c, t.Profile) {
					return nil, fmt.Errorf("profile %q is not allowed for browser %s", t.Profile, c.id)
				}
				return c, nil
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrBrowserGone, t.Instance)
	}
	// A browser whose token is limited to some profiles serves only those:
	// neither a bound match nor the default-browser fallback may reach a
	// profile outside its set, and a profile-less command (which any account
	// of a browser could answer) never goes to a limited browser.
	policy := pairedPolicies(conns)
	eligible := func(c *extConn) bool { return policy.allows(c, t.Profile) }
	var unbound *extConn
	var boundTo []string
	for _, c := range conns {
		if !eligible(c) {
			continue
		}
		set := c.boundProfiles()
		if t.Profile != "" && slices.Contains(set, t.Profile) {
			return c, nil
		}
		if len(set) == 0 {
			if unbound == nil {
				unbound = c
			}
		} else {
			boundTo = append(boundTo, set...)
		}
	}
	if unbound != nil {
		return unbound, nil
	}
	if t.Profile == "" {
		for _, c := range conns {
			if eligible(c) {
				return c, nil
			}
		}
		return nil, fmt.Errorf("%w: every connected browser is limited to named profiles; name one", ErrNoExtension)
	}
	sort.Strings(boundTo)
	return nil, &NoBrowserError{Profile: t.Profile, BoundTo: slices.Compact(boundTo)}
}

// ResolveTarget reports which browser t resolves to right now.
func (s *Server) ResolveTarget(t Target) (ConnInfo, error) {
	c, err := s.resolve(t)
	if err != nil {
		return ConnInfo{}, err
	}
	return c.info(), nil
}

// Browsers lists the connected browsers, newest first. Two browsers that
// run the same profile are both flagged Conflict.
func (s *Server) Browsers() []ConnInfo {
	conns := s.connList()
	infos := make([]ConnInfo, len(conns))
	perProfile := map[string]int{}
	for i, c := range conns {
		infos[i] = c.info()
		for _, p := range infos[i].ProfileIDs {
			perProfile[p]++
		}
	}
	for i := range infos {
		for _, p := range infos[i].ProfileIDs {
			if perProfile[p] > 1 {
				infos[i].Conflict = true
			}
		}
	}
	return infos
}
