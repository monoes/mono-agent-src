package extension

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Pairing without typing a token.
//
// A freshly installed extension has no token. When the bridge is up and the
// account is signed in, the extension asks GET /monoagent/status, then
// POST /monoagent/pair/claim. What the claim yields depends on the pairing
// mode (`extension autopair ask|auto|off`, setting extension.autoPair):
//
//   - ask (the default): the claim is only recorded as PENDING and a card
//     appears in the Human in Loop queue. The extension polls
//     GET /monoagent/pair/claim/<id> with its poll secret; the owner approves
//     with `monoagentcli extension approve` or on the card, and the poll that
//     sees the approval receives the token once (pairclaims.go);
//   - auto: the claim returns a token at once. For single-user machines only;
//   - off: no claims.
//
// Why ask is the default: the Origin header on which a claim relies is a
// string, not a credential. Any local process (curl -H 'Origin: chrome-
// extension://x'), any OTHER OS USER on this machine (loopback is shared) and
// any other extension with host access to 127.0.0.1 can send an extension
// origin of its own, and a token grants the powers of extension.token (0600,
// readable only by the owner): documents, vault, tasks, page control. A human
// decision is the only thing those callers cannot forge.
//
// Checks a claim or a poll must pass in every mode (otherwise 403 and nothing
// is recorded or handed out):
//   - the TCP peer is loopback and the Host header is a loopback name (a
//     DNS-rebound web page carries its own domain as Host);
//   - the Origin is chrome-extension:// or moz-extension:// with a host (a web
//     page carries its http(s) origin; no Origin is refused);
//   - Sec-Fetch-Site is not same-origin/same-site, Sec-Fetch-Mode not navigate;
//   - when MONOAGENT_EXTENSION_IDS lists extension ids, the Origin is one of them;
//   - the monoes.me account is signed in;
//   - the origin made fewer than claimsPerWindow claims in the last claimWindow
//     (per origin; the origin is a header the caller chooses, so this only
//     keeps one origin from hammering).
//
// Unpacked development builds have a path-derived id and need to be listed in
// MONOAGENT_EXTENSION_IDS when that list is used.

const (
	// AutoPairEnv can only make pairing stricter than the owner's setting
	// (off < ask < auto): 0/false/off/no or any value it does not recognise
	// means off, ask caps auto at ask, and auto never loosens a stored ask.
	AutoPairEnv = "MONOAGENT_EXTENSION_AUTOPAIR"
	// ExtensionIDsEnv is a comma-separated list of extension ids allowed to claim.
	ExtensionIDsEnv = "MONOAGENT_EXTENSION_IDS"

	claimsPerWindow = 5
	claimWindow     = time.Minute
	maxClaimBody    = 4 << 10
	// maxClaimOrigins bounds the per-origin rate table.
	maxClaimOrigins = 1024
)

// Pairing modes (`extension autopair`).
const (
	PairModeAsk  = "ask"
	PairModeAuto = "auto"
	PairModeOff  = "off"
)

// ParsePairMode reads a stored setting. Anything it does not recognise is ask,
// the safe default.
func ParsePairMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "off", "no":
		return PairModeOff
	case "auto":
		return PairModeAuto
	}
	return PairModeAsk
}

func pairModeRank(mode string) int {
	switch mode {
	case PairModeOff:
		return 0
	case PairModeAuto:
		return 2
	}
	return 1
}

// Where the effective pairing mode comes from.
const (
	PairSourceDefault  = "default"
	PairSourceSettings = "settings"
	PairSourceEnv      = "env"
)

// ResolvePairMode is the mode in force and where it comes from. The owner's
// stored setting (ask when unset) is the base; MONOAGENT_EXTENSION_AUTOPAIR
// can only tighten it, and a value it does not recognise means off.
func ResolvePairMode(stored string, storedSet bool) (mode, source string) {
	mode, source = PairModeAsk, PairSourceDefault
	if storedSet {
		mode, source = ParsePairMode(stored), PairSourceSettings
	}
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(AutoPairEnv)))
	if raw == "" {
		return mode, source
	}
	env := PairModeOff
	if raw == PairModeAsk || raw == PairModeAuto {
		env = raw
	}
	if pairModeRank(env) < pairModeRank(mode) {
		return env, PairSourceEnv
	}
	return mode, source
}

// SetPairMode installs the reader of the pairing mode setting. nil means ask.
func (s *Server) SetPairMode(mode func() string) {
	s.claimMu.Lock()
	s.pairMode = mode
	s.claimMu.Unlock()
}

// SetPairCards installs the Human in Loop card sink for pending claims.
func (s *Server) SetPairCards(c PairCards) {
	s.claimMu.Lock()
	s.pairCards = c
	s.claimMu.Unlock()
}

func (s *Server) cards() PairCards {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	return s.pairCards
}

// cardJob runs a card operation off the request path, in order: opening the
// database to file or close a card must not make a claim or a poll answer
// late (the extension gives the bridge about a second and a half).
func (s *Server) cardJob(fn func(PairCards)) {
	c := s.cards()
	if c == nil {
		return
	}
	s.cardOnce.Do(func() {
		s.cardJobs = make(chan func(), 64)
		go func() {
			for job := range s.cardJobs {
				job()
				s.cardWG.Done()
			}
		}()
	})
	s.cardWG.Add(1)
	select {
	case s.cardJobs <- func() { fn(c) }:
	default:
		s.cardWG.Done()
		s.logger.Warn().Msg("pairing card queue is full; the card is not filed (extension approve still works)")
	}
}

// flushCards waits for the queued card operations (tests).
func (s *Server) flushCards() { s.cardWG.Wait() }

// PairMode is the mode in force: the owner's setting, tightened by the environment.
func (s *Server) PairMode() string {
	s.claimMu.Lock()
	fn := s.pairMode
	s.claimMu.Unlock()
	stored := ""
	if fn != nil {
		stored = fn()
	}
	mode, _ := ResolvePairMode(stored, fn != nil)
	return mode
}

// claimOrigin returns the extension id of an Origin header, and whether it is
// an extension origin at all.
func claimOrigin(origin string) (id string, ok bool) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "chrome-extension" && u.Scheme != "moz-extension") || u.Host == "" {
		return "", false
	}
	return u.Host, true
}

func extensionIDAllowed(id string) bool {
	raw := strings.TrimSpace(os.Getenv(ExtensionIDsEnv))
	if raw == "" {
		return true
	}
	for _, a := range strings.Split(raw, ",") {
		if strings.TrimSpace(a) == id {
			return true
		}
	}
	return false
}

func loopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// claimAllowed runs the checks that do not need the account or the rate
// limit, and returns the reason it refuses ("" when it does not).
//
// A poll (GET) is sent by Chrome without an Origin header, so it may omit it;
// an Origin it does send must still be an allowed extension's. The poll's own
// proof is the claim id plus the 32-byte secret.
func claimAllowed(r *http.Request) (originID, refusal string) {
	if !loopbackPeer(r) || !loopbackHost(r.Host) {
		return "", "not a loopback request"
	}
	id, ok := claimOrigin(r.Header.Get("Origin"))
	if !ok && !(r.Method == http.MethodGet && r.Header.Get("Origin") == "") {
		return "", "not from a browser extension"
	}
	if ok && !extensionIDAllowed(id) {
		return "", "this extension id is not allowed to pair"
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "same-site":
		return "", "not from an extension"
	}
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return "", "not a fetch"
	}
	return id, ""
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ClaimStatus is what GET /monoagent/status answers: only enough for an
// extension to decide what to show. No names, no addresses, no token.
type ClaimStatus struct {
	Service  string `json:"service"`
	SignedIn bool   `json:"signedIn"`
	Paired   bool   `json:"paired"`
	PairMode string `json:"pairMode"`
}

// handleClaimStatus answers /monoagent/status.
func (s *Server) handleClaimStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !loopbackPeer(r) || !loopbackHost(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		if _, ok := claimOrigin(o); !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	writeJSONStatus(w, http.StatusOK, ClaimStatus{
		Service:  ServiceName,
		SignedIn: !accountRefuses(),
		Paired:   s.IsConnected(),
		PairMode: s.PairMode(),
	})
}

type claimRequest struct {
	Instance string `json:"instance"`
	Label    string `json:"label"`
	// Token is the browser's current token, if it has one: it lets a browser
	// that is already paired rotate its own token.
	Token string `json:"token"`
}

type claimReply struct {
	Token string `json:"token,omitempty"`
	ID    string `json:"id,omitempty"`
	// Code is the confirmation code to show while the request waits (see
	// PairCode); the owner types it to approve.
	Code string `json:"code,omitempty"`
	// Pending replies (ask mode): the extension polls with ClaimID and PollSecret.
	Pending    bool   `json:"pending,omitempty"`
	ClaimID    string `json:"claimId,omitempty"`
	PollSecret string `json:"pollSecret,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	// Status of a poll: pending, approved, denied or expired.
	Status string `json:"status,omitempty"`
}

// uaHints are tried in order: Edge and Opera also say Chrome.
var uaHints = []*regexp.Regexp{
	regexp.MustCompile(`Edg/[\d.]+`), regexp.MustCompile(`OPR/[\d.]+`),
	regexp.MustCompile(`Firefox/[\d.]+`), regexp.MustCompile(`Chrome/[\d.]+`),
}

// allowClaim is the per-origin sliding-window rate limit. An origin is a
// header a caller chooses, so the limit only keeps one origin from hammering;
// the pending queue bound is what keeps the owner's queue sane.
func (s *Server) allowClaim(origin string, now time.Time) bool {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.claimTimes == nil {
		s.claimTimes = map[string][]time.Time{}
	}
	if len(s.claimTimes) >= maxClaimOrigins {
		for o, ts := range s.claimTimes {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= claimWindow {
				delete(s.claimTimes, o)
			}
		}
		if _, known := s.claimTimes[origin]; !known && len(s.claimTimes) >= maxClaimOrigins {
			return false
		}
	}
	kept := s.claimTimes[origin][:0]
	for _, t := range s.claimTimes[origin] {
		if now.Sub(t) < claimWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= claimsPerWindow {
		s.claimTimes[origin] = kept
		return false
	}
	s.claimTimes[origin] = append(kept, now)
	return true
}

func (s *Server) claimGuard(w http.ResponseWriter, r *http.Request, method string) (originID string, ok bool) {
	refuse := func(code int, reason string) (string, bool) {
		s.logger.Warn().Str("remote", r.RemoteAddr).Str("origin", clip(r.Header.Get("Origin"))).Str("reason", reason).Msg("browser pairing claim refused")
		writeJSONStatus(w, code, map[string]string{"error": reason})
		return "", false
	}
	if r.Method != method {
		return refuse(http.StatusMethodNotAllowed, "method not allowed")
	}
	if s.PairMode() == PairModeOff {
		return refuse(http.StatusForbidden, "automatic pairing is turned off: run monoagentcli extension pair")
	}
	id, why := claimAllowed(r)
	if why != "" {
		return refuse(http.StatusForbidden, why)
	}
	if accountRefuses() {
		return refuse(http.StatusForbidden, "signed out: sign in to MonoAgent first")
	}
	return id, true
}

// handlePairClaim answers POST /monoagent/pair/claim.
func (s *Server) handlePairClaim(w http.ResponseWriter, r *http.Request) {
	originID, ok := s.claimGuard(w, r, http.MethodPost)
	if !ok {
		return
	}
	refuse := func(code int, reason string) {
		s.logger.Warn().Str("remote", r.RemoteAddr).Str("origin", clip(r.Header.Get("Origin"))).Str("reason", reason).Msg("browser pairing claim refused")
		writeJSONStatus(w, code, map[string]string{"error": reason})
	}
	origin := clip(r.Header.Get("Origin"))
	if !s.allowClaim(origin, time.Now()) {
		w.Header().Set("Retry-After", "60")
		refuse(http.StatusTooManyRequests, "too many pairing attempts; try again in a minute")
		return
	}
	var req claimRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxClaimBody))
	_ = json.Unmarshal(body, &req)
	if !validInstanceID.MatchString(req.Instance) {
		req.Instance = ""
	}
	agent := ""
	for _, re := range uaHints {
		if m := re.FindString(r.Header.Get("User-Agent")); m != "" {
			agent = m
			break
		}
	}
	label := cleanLabel(req.Label)
	if label == "" {
		label = agent
	}
	if label == "" {
		label = "browser " + originID
	}
	label = clip(label)
	if pairedBrowserCount() >= maxPairedBrowsers {
		refuse(http.StatusConflict, ErrBrowserLimit.Error())
		return
	}
	if s.PairMode() == PairModeAsk {
		s.expireClaims()
		c, secret, replaced, err := createPendingClaim(PendingClaim{Label: label, Origin: origin, Agent: agent, Instance: req.Instance})
		switch {
		case errors.Is(err, ErrClaimsFull):
			w.Header().Set("Retry-After", "60")
			refuse(http.StatusTooManyRequests, err.Error())
			return
		case err != nil:
			s.logger.Error().Err(err).Msg("browser pairing claim failed")
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not record the pairing request"})
			return
		}
		for _, id := range replaced {
			s.cardJob(func(cards PairCards) { cards.Settle(s.ctx, id, "expired") })
		}
		s.cardJob(func(cards PairCards) { cards.Open(s.ctx, c) })
		s.logger.Info().Str("claim", c.ID).Str("label", c.Label).Str("origin", c.Origin).
			Msg("browser asks to pair; approve with: monoagentcli extension approve " + c.ID)
		writeJSONStatus(w, http.StatusAccepted, claimReply{Pending: true, ClaimID: c.ID, PollSecret: secret, Code: PairCode(c.ID, secret), ExpiresAt: c.ExpiresAt.Format(time.RFC3339), Status: ClaimPending})
		return
	}
	token, entry, err := addPairedBrowser(PairedBrowser{
		Label: label, Origin: origin, Agent: agent, Instance: req.Instance,
		Restricted: true, // a new browser runs no profile until the owner allows one (a rotation keeps its own limit)
	}, mintOpts{RotateToken: req.Token})
	if err != nil {
		if errors.Is(err, ErrInstancePaired) || errors.Is(err, ErrBrowserLimit) {
			refuse(http.StatusConflict, err.Error())
			return
		}
		s.logger.Error().Err(err).Msg("browser pairing claim failed")
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not record the pairing"})
		return
	}
	s.logger.Info().Str("browser", entry.ID).Str("label", entry.Label).Str("origin", entry.Origin).
		Msg("browser paired automatically (revoke with: monoagentcli extension browsers revoke " + entry.ID + ")")
	writeJSONStatus(w, http.StatusOK, claimReply{Token: token, ID: entry.ID})
}

// handlePairPoll answers GET /monoagent/pair/claim/<id> with the poll secret
// as a bearer token. A wrong id, a wrong secret and another origin's claim
// all get the same 404.
func (s *Server) handlePairPoll(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.claimGuard(w, r, http.MethodGet); !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/monoagent/pair/claim/")
	secret := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	out, err := pollClaim(id, secret, clip(r.Header.Get("Origin")))
	switch {
	case errors.Is(err, ErrNoSuchClaim):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "no such pairing request"})
		return
	case errors.Is(err, ErrBrowserLimit):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		s.logger.Error().Err(err).Msg("browser pairing poll failed")
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read the pairing request"})
		return
	}
	if out.State != ClaimPending {
		s.cardJob(func(cards PairCards) { cards.Settle(s.ctx, out.Claim.ID, cardStatus(out.State)) })
	}
	switch out.State {
	case ClaimApproved:
		s.logger.Info().Str("browser", out.Entry.ID).Str("label", out.Entry.Label).
			Msg("browser paired after approval (revoke with: monoagentcli extension browsers revoke " + out.Entry.ID + ")")
		writeJSONStatus(w, http.StatusOK, claimReply{Status: ClaimApproved, Token: out.Token, ID: out.Entry.ID})
	case "expired":
		writeJSONStatus(w, http.StatusGone, claimReply{Status: "expired"})
	default:
		writeJSONStatus(w, http.StatusOK, claimReply{Status: out.State})
	}
}

func cardStatus(state string) string {
	switch state {
	case ClaimApproved:
		return "approved"
	case ClaimDenied:
		return "rejected"
	}
	return "expired"
}

// expireClaims drops claims past their time and closes their cards.
func (s *Server) expireClaims() {
	for _, id := range expiredClaimIDs() {
		s.cardJob(func(cards PairCards) { cards.Settle(s.ctx, id, "expired") })
	}
}

// sweepRevoked closes the sockets whose per-browser token has been revoked.
func (s *Server) sweepRevoked() {
	live := pairedBrowserIDs()
	if live == nil {
		return // the state could not be read: close nothing
	}
	s.connMu.Lock()
	var drop []*extConn
	for _, c := range s.conns {
		if c.tokenID != "" && !live[c.tokenID] {
			drop = append(drop, c)
		}
	}
	s.connMu.Unlock()
	for _, c := range drop {
		s.logger.Warn().Str("browser", c.tokenID).Msg("closing the socket of a revoked browser")
		_ = c.ws.Close()
	}
}
