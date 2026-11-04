package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

	// vaultRate is vault.save's rate windows, set by SetVaultSink.
	vaultRate *saveRateLimiter

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
	// profileSource is profile.list's source, kept so a capture naming a
	// deleted profile is caught before it is written (fileUnderLiveProfile).
	profileSource ProfileSource

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

	// The zero-step claim flow (autopair.go): the kill-switch reader and the
	// rate-limit window.
	claimMu    sync.Mutex
	pairMode   func() string
	pairCards  PairCards
	cardOnce   sync.Once
	cardJobs   chan func()
	cardWG     sync.WaitGroup
	claimTimes map[string][]time.Time

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
	raw := PortOverride()
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
	go sweepChatImages(os.TempDir(), chatImagesMaxAge, time.Now())

	mux := http.NewServeMux()
	mux.HandleFunc("/monoagent", s.handleWS)
	mux.HandleFunc("/monoagent/health", s.handleHealth)
	mux.HandleFunc("/monoagent/relay", s.handleRelay)
	mux.HandleFunc("/monoagent/auth", s.handleAuthProbe)
	mux.HandleFunc("/monoagent/cdp", s.handleCdpSocket)
	mux.HandleFunc("/monoagent/pair", s.handlePairPage)
	mux.HandleFunc("/monoagent/pair/exchange", s.handlePairExchange)
	mux.HandleFunc("/monoagent/pair/nonce", s.handlePairNonce)
	mux.HandleFunc("/monoagent/status", s.handleClaimStatus)
	mux.HandleFunc("/monoagent/pair/claim", s.handlePairClaim)
	mux.HandleFunc("/monoagent/pair/claim/", s.handlePairPoll)
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

// handleHealth reports whether this server is alive and whether it currently
// holds a live extension connection. Other local monoagentcli processes probe
// this before deciding whether to relay through this server instead of
// starting their own (which would otherwise race for the same port and leave
// the extension connected to only one of them).
//
// It also answers the extension popup's "what is going on?" — see Status in
// status.go for the shape and for why it is safe to serve without auth.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := s.Status()
	if r.URL.Query().Has(nonceParam) {
		nonce := r.URL.Query().Get(nonceParam)
		if !validNonce(nonce) {
			http.Error(w, "bad nonce", http.StatusBadRequest)
			return
		}
		if addr, ok := s.Addr(); ok && s.token != "" {
			_, port, _ := net.SplitHostPort(addr)
			st.Proof = proofFor(s.token, nonce, healthPath, requestHost(r), port)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// Addr returns the address this server actually bound (which may differ
// from the requested one on EADDRINUSE fallback), and whether Start has
// gotten far enough to know it yet.
func (s *Server) Addr() (string, bool) {
	s.addrMu.Lock()
	defer s.addrMu.Unlock()
	return s.boundAddr, s.boundAddr != ""
}
