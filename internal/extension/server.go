package extension

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/capture"
)

// Server is a WebSocket server that accepts a single connection from the
// Chrome Extension and dispatches commands/responses.
type Server struct {
	addr string
	// conns holds one socket per connected browser, keyed by the instance
	// id its extension reports (see conns.go). Guarded by connMu.
	conns   map[string]*extConn
	connMu  sync.Mutex
	pending map[string]chan *Response
	pendMu  sync.Mutex

	connected chan struct{} // closed when first connection arrives
	connOnce  sync.Once

	// Capture plumbing (see capture.go). streams carries the multi-message
	// page_capture responses, which pending cannot: it is single-shot, and
	// one capture may span many messages under one id. Both are guarded by
	// pendMu, as is onCapture.
	streams        map[string]chan *Response
	assembler      *capture.Assembler
	captureWriter  *capture.Writer
	captureInbox   string
	captureOptions capture.Options
	onCapture      func(*capture.Result, error)
	afterWrite     func(*capture.Result)

	// token authenticates /monoagent/relay requests (see handleRelay). Set
	// once Start has won the port bind; empty (and thus relay-rejecting)
	// before that.
	token string

	// cdpClients are the sockets subscribed to the CDP relay (see cdp.go).
	// Debugger events are fanned out to all of them; command replies go
	// only to the client that asked.
	cdpClients map[*cdpClient]struct{}
	cdpMu      sync.Mutex

	// boundAddr is the address actually bound (may differ from the
	// requested addr on EADDRINUSE fallback — see listenCandidates). Set
	// alongside token, once Start has won the port bind; empty before that.
	boundAddr string
	addrMu    sync.Mutex

	// handlerState is the extension→Go request channel (see request.go):
	// the method registry and the in-flight bound. Embedded so the channel
	// reads as one unit in its own file rather than as four more fields
	// here.
	handlerState

	// knowledgeState is the monomind runner those handlers ask (see
	// knowledge.go).
	knowledgeState

	// recordingState ingests kind:"recording" frames and runs the
	// record.* request methods (see recording.go).
	recordingState

	// pairingNonces backs the one-time, loopback-only auto-pairing flow
	// (see handlePairPage/handlePairExchange): a short-lived, single-use
	// nonce that exchanges for the real token, so the long-lived secret
	// itself never has to appear in a URL or browser history.
	pairingNonces map[string]pairingNonceEntry
	pairingMu     sync.Mutex

	// The self-description served at /monoagent/health (see status.go).
	// lastAuthFailure is what lets that endpoint say "running, but this
	// extension is not paired with me" instead of a bare "not connected".
	startedAt       time.Time
	version         string
	lastAuthFailure time.Time
	statusMu        sync.Mutex

	logger zerolog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	server *http.Server
}

// pairingNonceEntry is one outstanding auto-pairing nonce.
type pairingNonceEntry struct {
	token     string
	expiresAt time.Time
}

// pairingNonceTTL bounds how long an auto-pairing nonce stays valid — long
// enough to cover the OS actually opening a browser tab, short enough that
// a stale nonce (tab left open, browser opened slowly) can't be reused
// later.
const pairingNonceTTL = 2 * time.Minute

var upgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

// pongWait is how long the server waits for a pong (or any message) before
// giving up on a connection; pingInterval is how often it probes, kept well
// under pongWait so a healthy extension always has time to reply. Without
// this, a connection that dies without a clean TCP close (extension service
// worker suspended, laptop sleep, network drop) leaves s.conn non-nil
// forever: IsConnected() keeps reporting true while the extension is
// actually gone.
const (
	pongWait     = 30 * time.Second
	pingInterval = (pongWait * 9) / 10
)

// maxMessageSize bounds every frame gorilla/websocket will accept on the
// extension connection (auth frame and responses alike); gorilla applies no
// limit by default, so an unbounded or malicious peer could otherwise send
// an arbitrarily large message and force the server to buffer it entirely
// in memory. 32MB comfortably covers the largest legitimate response
// (full-page HTML/text dumps) with headroom.
const maxMessageSize = 32 << 20

// checkOrigin restricts the extension control channel to same-machine callers:
// native clients that send no Origin, the browser extension itself
// (chrome-extension:// / moz-extension://), and loopback origins. Arbitrary
// websites the user visits carry a public Origin and are rejected, so a page
// cannot open ws://127.0.0.1:9222/monoagent and impersonate the extension.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme == "chrome-extension" || u.Scheme == "moz-extension" {
		return true
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// loopbackAddr forces a missing or wildcard host to bind to loopback only, so
// the unauthenticated extension control channel is never reachable from other
// hosts on the network.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// DefaultExtensionPort is the port the extension server and the Chrome
// extension both prefer; FallbackExtensionPort is tried instead when that
// port is already taken — most commonly by a Chrome started with
// --remote-debugging-port=9222, which also speaks WebSocket there.
const (
	DefaultExtensionPort  = "9222"
	FallbackExtensionPort = "9323"
)

// ExtensionPortEnv overrides the default extension listen port (validated
// integer). When set, the EADDRINUSE fallback does not apply: an explicit
// port that is busy is an operator error worth surfacing, not papering over.
const ExtensionPortEnv = "MONOAGENT_EXTENSION_PORT"

// resolveListenAddr applies ExtensionPortEnv to addr, replacing its port.
func resolveListenAddr(addr string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(ExtensionPortEnv))
	if raw == "" {
		return addr, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid %s %q: want an integer between 1 and 65535", ExtensionPortEnv, raw)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port component to override.
		return addr, nil
	}
	return net.JoinHostPort(host, raw), nil
}

// listenCandidates returns the bind addresses to try for addr, in order:
// the (env-overridden) address itself, plus the fallback port when the
// primary is the default port.
func listenCandidates(addr string) ([]string, error) {
	addr, err := resolveListenAddr(addr)
	if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return []string{addr}, nil
	}
	if port == DefaultExtensionPort {
		return []string{addr, net.JoinHostPort(host, FallbackExtensionPort)}, nil
	}
	return []string{addr}, nil
}

// tryListen binds the first address in addrs it can. Only EADDRINUSE moves
// on to the next candidate; any other error (e.g. permission denied) fails
// immediately with that error.
func tryListen(addrs []string) (net.Listener, string, error) {
	var lastErr error
	for _, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err == nil {
			return l, a, nil
		}
		lastErr = err
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, "", err
		}
	}
	return nil, "", lastErr
}

// NewServer creates a new extension WebSocket server. Addr should be a
// host:port string such as ":9222".
func NewServer(addr string, logger zerolog.Logger) *Server {
	s := &Server{
		addr:      addr,
		pending:   make(map[string]chan *Response),
		connected: make(chan struct{}),
		startedAt: time.Now(),
		logger:    logger.With().Str("component", "extension-server").Logger(),
	}
	// The extension→Go request channel is on by default (see request.go).
	// Nothing at the call site has to switch it on: a question the browser
	// can only ask when someone remembered to wire it up is a question
	// that silently goes unanswered on most installs.
	s.registerBuiltinHandlers()
	return s
}

// Start starts the HTTP/WebSocket server and blocks until the context is
// cancelled or the server shuts down.
func (s *Server) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/monoagent", s.handleWS)
	mux.HandleFunc("/monoagent/health", s.handleHealth)
	mux.HandleFunc("/monoagent/relay", s.handleRelay)
	mux.HandleFunc("/monoagent/auth", s.handleAuthProbe)
	mux.HandleFunc("/monoagent/cdp", s.handleCdpSocket)
	mux.HandleFunc("/monoagent/pair", s.handlePairPage)
	mux.HandleFunc("/monoagent/pair/exchange", s.handlePairExchange)
	mux.HandleFunc("/monoagent/resolve", s.handleResolve)
	mux.HandleFunc("/monoagent/browsers", s.handleBrowsers)

	addr := loopbackAddr(s.addr)
	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-s.ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutCancel()
		_ = s.server.Shutdown(shutCtx)
	}()

	// Listen and Serve are split (instead of the equivalent ListenAndServe)
	// so the token is only generated after this process has actually won
	// the port bind — a process that loses the bind never writes a token
	// that could otherwise race with and clobber the winner's. On EADDRINUSE
	// for the default port the fallback (see listenCandidates) is tried
	// before giving up, so a Chrome holding --remote-debugging-port=9222
	// no longer blocks the extension channel entirely.
	candidates, err := listenCandidates(addr)
	if err != nil {
		return err
	}
	listener, boundAddr, err := tryListen(candidates)
	if err != nil {
		return err
	}
	if boundAddr != candidates[0] {
		s.logger.Warn().Str("addr", boundAddr).
			Msgf("extension port %s busy, fell back to %s", candidates[0], boundAddr)
	}
	addr = boundAddr
	s.server.Addr = addr

	token, err := loadOrCreateToken()
	if err != nil {
		listener.Close()
		return fmt.Errorf("load extension relay token: %w", err)
	}
	s.token = token
	s.addrMu.Lock()
	s.boundAddr = addr
	s.addrMu.Unlock()

	s.logger.Info().Str("addr", addr).Msg("extension server listening")
	// Recordings outlive connections (the service worker is suspended
	// constantly), so their idle reaper runs for the server's lifetime,
	// not the socket's.
	s.startRecordingReaper(s.ctx)
	err = s.server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// StartAsync starts the server in a background goroutine. The returned channel
// receives the Start error (most commonly "address already in use" when another
// process — the daemon, the GUI, or a second CLI invocation — already owns the
// extension port) so callers can fall back to relaying through that process
// instead of waiting on a server that never came up.
func (s *Server) StartAsync(ctx context.Context) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.Start(ctx); err != nil {
			s.logger.Error().Err(err).Msg("extension server error")
			errCh <- err
		}
		close(errCh)
	}()
	return errCh
}

// WaitForConnection blocks until the Chrome extension connects or the timeout
// expires.
func (s *Server) WaitForConnection(timeout time.Duration) error {
	select {
	case <-s.connected:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("extension did not connect within %s", timeout)
	}
}

// IsConnected reports whether at least one browser's extension is
// connected. Which one a command reaches is resolve's business.
func (s *Server) IsConnected() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return len(s.conns) > 0
}

// SendCommand sends a command to the default browser (see resolve) and
// waits for the matching response.
func (s *Server) SendCommand(cmd *Command, timeout time.Duration) (*Response, error) {
	return s.SendCommandTo(Target{}, cmd, timeout)
}

// CreateTab opens a tab in the default browser.
func (s *Server) CreateTab(url string) (int, error) { return s.CreateTabFor(Target{}, url) }

// CloseTab closes a tab in the default browser.
func (s *Server) CloseTab(tabID int) error { return s.CloseTabFor(Target{}, tabID) }

// Close gracefully shuts down the server and closes the WebSocket connection.
func (s *Server) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	// The recording reaper exits on the cancel; wait so nothing of this
	// server is still running (or writing an envelope) once Close returns.
	s.waitRecordingReaper()
	s.connMu.Lock()
	conns := s.conns
	s.conns = nil
	s.connMu.Unlock()

	var firstErr error
	for _, c := range conns {
		if err := c.ws.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// authTimeout bounds how long a newly-upgraded socket has to send its auth
// frame before the server gives up and closes it.
const authTimeout = 5 * time.Second

// unauthorizedCloseCode is sent to a socket that fails the auth handshake,
// so the extension (which knows this code) can distinguish "wrong/missing
// token, needs pairing" from a generic disconnect.
const unauthorizedCloseCode = 4401

// authFrame is the first message an extension socket must send after
// upgrade. Anything else — wrong type, wrong/missing token, or no message
// within authTimeout — gets the connection closed without ever being
// installed, so an unauthenticated client can never replace an
// already-authenticated one.
//
// Instance, Profile, Label and Version come from extension ≥1.5 (see
// chrome-extension/browser_binding.js). An older extension omits them and
// lands in the legacy slot; an older bridge ignores them.
type authFrame struct {
	Type     string `json:"type"`
	Token    string `json:"token"`
	Instance string `json:"instance,omitempty"`
	Profile  string `json:"profile,omitempty"`
	// Profiles is the whole set (extension >= 1.8.0); Profile is its first.
	Profiles []string `json:"profiles,omitempty"`
	Label    string   `json:"label,omitempty"`
	Version  string   `json:"version,omitempty"`
}

// handleWS upgrades an incoming HTTP request to a WebSocket connection and
// authenticates it before treating it as the extension connection. The
// control channel otherwise has no identity check beyond same-machine
// Origin (see checkOrigin) — any local process could open it and either
// receive privileged automation commands meant for the real extension or
// knock the real extension's connection out from under it.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error().Err(err).Msg("websocket upgrade failed")
		return
	}
	conn.SetReadLimit(maxMessageSize)

	auth, ok := s.authenticate(conn)
	if !ok {
		_ = conn.Close()
		return
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Only reached after authenticate succeeds, so an unauthenticated
	// socket never replaces anything.
	c := newExtConn(conn, auth, time.Now())
	s.installConn(c)

	s.connOnce.Do(func() { close(s.connected) })
	s.logger.Info().Str("remote", conn.RemoteAddr().String()).Str("instance", c.id).
		Strs("profiles", c.boundProfiles()).Msg("extension connected")

	done := make(chan struct{})
	go s.pingLoop(c, done)
	s.readLoop(c)
	close(done)
}

// authenticate reads exactly one frame from a freshly-upgraded connection
// and requires it to be a well-formed authFrame carrying the current
// extension token, compared in constant time. It never installs the connection:
// callers are responsible for installing the connection only on a true
// result.
func (s *Server) authenticate(conn *websocket.Conn) (authFrame, bool) {
	conn.SetReadDeadline(time.Now().Add(authTimeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		s.noteAuthFailure()
		s.logger.Warn().Err(err).Msg("extension connection closed before authenticating")
		return authFrame{}, false
	}

	var auth authFrame
	ok := json.Unmarshal(msg, &auth) == nil &&
		auth.Type == "auth" &&
		subtle.ConstantTimeCompare([]byte(auth.Token), []byte(s.token)) == 1
	if !ok {
		s.noteAuthFailure()
		s.logger.Warn().Msg("rejected extension connection: bad or missing auth token")
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(unauthorizedCloseCode, "unauthorized"),
			time.Now().Add(time.Second))
		return authFrame{}, false
	}
	return auth, true
}

// pingLoop periodically pings one browser's socket so a dead peer is
// detected via the read deadline in handleWS/readLoop instead of hanging
// indefinitely.
func (s *Server) pingLoop(c *extConn, done <-chan struct{}) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			// Piggybacked on the ping: a capture whose chunks stopped
			// arriving has nobody waiting on it when it was the extension
			// flushing its queue, so something has to time it out.
			s.sweepCaptures()
			if err := c.ping(); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// handleHealth reports whether this server is alive and whether it currently
// holds a live extension connection. Other local monoagentcli processes probe
// this before deciding whether to relay through this server instead of
// starting their own (which would otherwise race for the same port and leave
// the extension connected to only one of them).
//
// It also answers the extension popup's "what is going on?" — see Status in
// status.go for the shape and for why it is safe to serve without auth.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Status())
}

// Addr returns the address this server actually bound (which may differ
// from the requested one on EADDRINUSE fallback), and whether Start has
// gotten far enough to know it yet.
func (s *Server) Addr() (string, bool) {
	s.addrMu.Lock()
	defer s.addrMu.Unlock()
	return s.boundAddr, s.boundAddr != ""
}

// CreatePairingNonce issues a short-lived, single-use nonce that exchanges
// for the real extension token via handlePairExchange — see PairingURL in
// exec.go for why: it lets the auto-pairing flow put a URL in the user's
// browser (and history) without ever putting the long-lived pairing token
// there. Opportunistically prunes expired nonces so this map can't grow
// unbounded across a long-running daemon's lifetime.
func (s *Server) CreatePairingNonce() (string, error) {
	if s.token == "" {
		return "", fmt.Errorf("extension server: not ready (no token yet)")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate pairing nonce: %w", err)
	}
	nonce := hex.EncodeToString(buf)

	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	if s.pairingNonces == nil {
		s.pairingNonces = make(map[string]pairingNonceEntry)
	}
	now := time.Now()
	for n, e := range s.pairingNonces {
		if now.After(e.expiresAt) {
			delete(s.pairingNonces, n)
		}
	}
	s.pairingNonces[nonce] = pairingNonceEntry{token: s.token, expiresAt: now.Add(pairingNonceTTL)}
	return nonce, nil
}

// exchangePairingNonce consumes a nonce (valid exactly once) and returns the
// real token it maps to.
func (s *Server) exchangePairingNonce(nonce string) (string, bool) {
	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	entry, ok := s.pairingNonces[nonce]
	if ok {
		delete(s.pairingNonces, nonce) // single-use regardless of expiry outcome
	}
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.token, true
}

// handlePairPage serves the auto-pairing landing page the CLI opens in the
// user's browser while ensureExtensionConnected waits for a connection.
// Pairing itself happens via the content script manifest.json declares for
// this exact path (chrome-extension/pair_bridge.js) — this handler only
// needs to render feedback; it never sees or handles the token itself.
func (s *Server) handlePairPage(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("n") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><p>Missing pairing code.</p>`))
		return
	}
	_, _ = w.Write([]byte(pairPageHTML))
}

// handlePairExchange consumes a one-time nonce (query param "n") and
// returns the real pairing token as {"token": "..."}, or 404 if the nonce
// is missing, unknown, already used, or expired. Applies the same Origin
// policy as the WebSocket endpoint (checkOrigin) — same-machine callers
// only. That check is still no stronger than the WebSocket's own (any
// chrome-extension:// or loopback origin passes); the nonce's single-use,
// short TTL is what actually bounds exposure beyond that shared baseline.
func (s *Server) handlePairExchange(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	nonce := r.URL.Query().Get("n")
	token, ok := s.exchangePairingNonce(nonce)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown or expired pairing code"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// pairPageHTML is the static body handlePairPage serves. All the real work
// happens in the injected content script; this is purely user feedback.
const pairPageHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>MonoAgent Bridge — Pairing</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; background: #0b0f14; color: #e2e8f0;
         display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
  #box { text-align: center; }
  #status { font-size: 15px; margin-top: 12px; color: #94a3b8; }
</style>
</head>
<body>
  <div id="box">
    <div style="font-size:20px;font-weight:600;">MonoAgent Bridge</div>
    <div id="status">Pairing…</div>
  </div>
  <script>
    // The real work is done by the extension's own content script
    // (pair_bridge.js), injected automatically because this exact path is
    // declared in manifest.json's content_scripts. It posts progress back
    // to this page via window.postMessage so this tab can self-close.
    let heardFromBridge = false;
    window.addEventListener("message", (ev) => {
      if (ev.source !== window || !ev.data || ev.data.source !== "monoagent-pair-bridge") return;
      heardFromBridge = true;
      const status = document.getElementById("status");
      if (ev.data.ok) {
        status.textContent = "Paired! You can close this tab.";
        setTimeout(() => window.close(), 1500);
      } else {
        status.textContent = "Pairing failed: " + (ev.data.error || "unknown error") +
          " — paste the token manually in the extension popup instead.";
      }
    });
    // If the content script never fires at all — extension disabled, not
    // yet reloaded after an update (content_scripts changes need a manual
    // reload), or this page was opened in a browser without the extension
    // installed — the page would otherwise say "Pairing…" forever with no
    // indication anything is wrong. Surface that after a few seconds
    // instead of hanging silently.
    setTimeout(() => {
      if (heardFromBridge) return;
      document.getElementById("status").textContent =
        "Still waiting — make sure the MonoAgent Bridge extension is installed, enabled, " +
        "and reloaded at chrome://extensions (or the equivalent in your browser), " +
        "or paste the token manually in the extension popup instead.";
    }, 8000);
  </script>
</body>
</html>`

// handleRelay lets another local monoagentcli process dispatch a Command
// through this server's live extension connection and get the Response back,
// without needing to own the WebSocket connection itself. This is what makes
// it safe for a short-lived CLI invocation to share the daemon's already-
// connected extension instead of starting a second, competing server.
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	// Same-machine callers only. Local clients (the CLI, the node runtime,
	// the app's bundled CLI, the MCP server) send no Origin, which
	// checkOrigin accepts; a web page's Origin, or a DNS-rebound Host, is
	// refused before the token is looked at.
	if !checkOrigin(r) || !loopbackHost(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// After the token, so a caller without it learns nothing of the account.
	if accountRefuses() {
		writeRelayLocked(w)
		return
	}
	var cmd Command
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, fmt.Sprintf("invalid command: %v", err), http.StatusBadRequest)
		return
	}
	timeout := defaultTimeout
	if ms := r.URL.Query().Get("timeout_ms"); ms != "" {
		if n, err := time.ParseDuration(ms + "ms"); err == nil {
			timeout = n
		}
	}
	// Which browser, for callers new enough to say. An older caller sends
	// neither parameter and gets the default browser, as it always did.
	target := targetFromQuery(r.URL.Query())
	// A relayed capture is executed and *written* here, by the process
	// that owns the extension connection, so the caller gets back a small
	// Result instead of a multi-megabyte envelope over loopback HTTP.
	if cmd.Type == CmdPageCapture {
		s.serveRelayCapture(w, target, &cmd, timeout, r.URL.Query().Get("inbox"))
		return
	}
	resp, err := s.SendCommandTo(target, &cmd, timeout)
	w.Header().Set("Content-Type", "application/json")
	if resp == nil {
		resp = &Response{ID: cmd.ID}
	}
	if err != nil && resp.Error == "" {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// readLoop reads messages from the WebSocket and dispatches responses to
// waiting callers.
func (s *Server) readLoop(c *extConn) {
	defer func() {
		s.removeConn(c)
		_ = c.ws.Close()
		s.logger.Info().Str("instance", c.id).Msg("extension disconnected")
	}()

	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			// CloseAbnormalClosure/CloseNoStatusReceived are on this list
			// because that is what Chrome suspending an idle MV3 service
			// worker looks like from here: the socket simply stops, with
			// no close frame. It happens constantly and by design, so it
			// is not an error — it is the extension being an extension.
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure,
				websocket.CloseNoStatusReceived) {
				s.logger.Error().Err(err).Msg("websocket read error")
			}
			return
		}

		// A frame the extension originated, expecting an answer back
		// (see request.go). Checked before the Response decode because a
		// request has no `success` field and would otherwise land in
		// dispatch as an unmatched failure.
		switch frameKind(msg) {
		case KindRequest:
			s.serveRequest(c, msg)
			continue
		case KindRecording:
			// An activity-recording frame (recording.go): a push that
			// is acked, never a response to anything this process sent.
			s.serveRecording(c, msg)
			continue
		case KindBinding:
			s.serveBinding(c, msg)
			continue
		}

		var resp Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			// Never log the raw payload: extension responses (get_cookies,
			// eval results, page text) can carry live session cookies or
			// other page-derived secrets, and logs are frequently retained,
			// uploaded in support bundles, or shipped to observability tools.
			s.logger.Error().Err(err).Int("len", len(msg)).Msg("invalid response JSON")
			continue
		}

		s.logger.Debug().Str("id", resp.ID).Bool("success", resp.Success).Str("error", resp.Error).Msg("response received")

		s.dispatch(c, &resp)
	}
}
