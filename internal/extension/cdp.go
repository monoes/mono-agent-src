package extension

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/monoes/mono-agent/internal/accountdoor"
)

// The CDP relay: raw Chrome DevTools Protocol over the extension bridge.
//
// The extension already holds Chrome's `debugger` permission and already
// speaks CDP to the user's real, logged-in tabs (see the eval_cdp/type_cdp
// commands in chrome-extension/background.js, and cdp_proxy.js which
// generalises them). What was missing was a way for something *outside* this
// process to drive that — specifically monobrowse, whose every instrument
// (console, network, HAR, vitals, trace, CPU profiler, snapshot, screenshot,
// emulation) is written against a CDP client and none of which should have
// to be ported per backend.
//
// So: one WebSocket endpoint that carries CDP envelopes to the extension and
// fans debugger events back. It deliberately knows nothing about CDP itself —
// framing, ids, sessions and timeouts all live in monobrowse's bridge
// transport (packages/@monoes/monobrowse/src/browser/bridge.ts). This side is
// a relay, and a relay that parsed CDP would be a second implementation to
// keep in sync.
//
// One thing it enforces, and it is the only boundary here: the same token
// as /monoagent/relay. This socket reaches a browser the user is signed
// into; it is not a public port, and the token is what keeps it off one.
//
// It also accepts only the three cdp_* envelope types — be precise about
// what that buys, because the shape of the check invites a stronger reading
// than it deserves. It keeps this endpoint from becoming a second path to
// the extension's OWN commands (get_cookies, eval, page_capture), which
// have their own handling and audit trail on the general relay. It does
// NOT narrow what CDP itself can do: the extension side forwards whatever
// `method` an envelope names, with no allowlist (chrome-extension/
// cdp_proxy.js), so anything holding the token can call Runtime.evaluate or
// Network.getAllCookies through here and get exactly what it asked for —
// which is by design, since the point is to run monobrowse's instruments
// unmodified, and those speak the whole protocol.
//
// So: a routing gate, not a confinement boundary. Anyone reasoning about
// what a compromised relay client can reach should reason about the token
// and about chrome.debugger, not about this list of three.
const (
	// CmdCdp carries one CDP command through chrome.debugger.sendCommand.
	CmdCdp = "cdp"
	// CmdCdpAttach attaches the debugger to a tab and starts relaying its
	// events. With no tabId the extension resolves the active tab and
	// reports back which one it took.
	CmdCdpAttach = "cdp_attach"
	// CmdCdpDetach stops the relay and detaches, which is what takes
	// Chrome's "started debugging this browser" banner off the tab.
	CmdCdpDetach = "cdp_detach"

	// CdpEventType marks an unsolicited frame carrying a debugger event.
	// Like a flushed capture (see isCaptureResponse) it matches no pending
	// command, so the read loop needs the marker to tell it apart from a
	// stray response.
	CdpEventType = "cdp_event"
)

// cdpCommandTimeout bounds one relayed CDP command. Generous next to a local
// socket's round trip because the path is longer — HTTP/WS to this process,
// WS to an MV3 service worker that may have to be woken, then
// chrome.debugger — and because a screenshot or a large DOM snapshot is a
// multi-megabyte frame at the far end.
const cdpCommandTimeout = 60 * time.Second

// maxInflightCdpCommands bounds the goroutines one relay socket can have
// waiting on the extension at once. Commands are relayed concurrently
// (monobrowse pipelines several domain enables at a time), so this is what
// keeps a chatty or hostile client from fanning out without limit.
const maxInflightCdpCommands = 64

// cdpWriteTimeout bounds one write to a relay client. Without it a client
// that has stopped reading its socket parks whichever goroutine is writing
// to it for as long as it stays connected: loopback TCP has no timeout of
// its own, so a SIGSTOPped monobrowse blocks forever rather than failing.
const cdpWriteTimeout = 10 * time.Second

// cdpCloseTimeout bounds the courtesy close frame sent to a client being
// dropped. Short, and skipped entirely if a write is already in flight —
// telling a wedged client why it is going is worth a moment, never a wait.
const cdpCloseTimeout = time.Second

// cdpEventBacklog and cdpEventBacklogBytes bound what one client may have
// queued but unwritten. Both: a trace chunk is megabytes and a console
// message is bytes, so a frame count alone would let one client hold
// gigabytes (maxMessageSize is 32MB), and a byte budget alone would let a
// flood of tiny events queue without limit.
const (
	cdpEventBacklog      = 256
	cdpEventBacklogBytes = 8 << 20
)

// cdpClient is one connected consumer of the relay — in practice a
// monobrowse CdpClient behind its bridge transport.
type cdpClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	// events is this client's backlog: fanoutCdpEvent hands frames over
	// without ever blocking, and writeEvents puts them on the socket. The
	// queue exists so that the extension read loop — the only thing
	// draining the extension connection — is never the goroutine waiting
	// on a consumer.
	events   chan []byte
	queued   atomic.Int64
	done     chan struct{}
	doneOnce sync.Once
	// tabs this client attached, so they can be detached if it vanishes.
	tabsMu sync.Mutex
	tabs   map[int]struct{}
	// target is the one browser this client drives. It is resolved once, at
	// connect, so every command and every event stays in one browser even
	// though tab ids repeat across browsers. The zero Target (nothing was
	// connected yet) resolves per command, and events from any browser
	// reach it, which is the pre-routing behaviour.
	target Target
}

func newCdpClient(conn *websocket.Conn) *cdpClient {
	return &cdpClient{
		conn:   conn,
		events: make(chan []byte, cdpEventBacklog),
		done:   make(chan struct{}),
		tabs:   make(map[int]struct{}),
	}
}

func (c *cdpClient) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(data)
}

// writeFrame is the only path to the socket, and every write through it is
// bounded by cdpWriteTimeout.
func (c *cdpClient) writeFrame(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(cdpWriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// queueEvent hands one already-marshalled event frame to this client's
// writer. It never blocks; false means the backlog is full.
func (c *cdpClient) queueEvent(frame []byte) bool {
	select {
	case <-c.done:
		return true // already going away; not a backlog problem
	default:
	}
	size := int64(len(frame))
	if c.queued.Add(size) > cdpEventBacklogBytes {
		c.queued.Add(-size)
		return false
	}
	select {
	case c.events <- frame:
		return true
	default:
		c.queued.Add(-size)
		return false
	}
}

// writeEvents drains the backlog onto the socket, on its own goroutine, so
// that a slow consumer costs this client and nothing else. A failed write
// means the client is gone or wedged past cdpWriteTimeout; either way it is
// dropped and its read loop unwinds.
func (c *cdpClient) writeEvents() {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.events:
			c.queued.Add(-int64(len(frame)))
			if err := c.writeFrame(frame); err != nil {
				c.drop("")
				return
			}
		}
	}
}

// drop disconnects this client. Callable from the extension read loop
// (fanoutCdpEvent does) because it never waits: the close frame and the
// socket close happen on their own goroutine, and the close frame is
// skipped rather than queued behind a write already stuck on the socket.
func (c *cdpClient) drop(reason string) {
	c.doneOnce.Do(func() {
		close(c.done)
		go func() {
			if reason != "" && c.writeMu.TryLock() {
				_ = c.conn.SetWriteDeadline(time.Now().Add(cdpCloseTimeout))
				_ = c.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseTryAgainLater, reason))
				c.writeMu.Unlock()
			}
			// Unblocks any write still parked on this socket, which is what
			// lets the client's read loop return and clean up.
			_ = c.conn.Close()
		}()
	})
}

func (c *cdpClient) rememberTab(tabID int) {
	if tabID == 0 {
		return
	}
	c.tabsMu.Lock()
	defer c.tabsMu.Unlock()
	c.tabs[tabID] = struct{}{}
}

func (c *cdpClient) forgetTab(tabID int) {
	c.tabsMu.Lock()
	defer c.tabsMu.Unlock()
	delete(c.tabs, tabID)
}

func (c *cdpClient) attachedTabs() []int {
	c.tabsMu.Lock()
	defer c.tabsMu.Unlock()
	out := make([]int, 0, len(c.tabs))
	for id := range c.tabs {
		out = append(out, id)
	}
	return out
}

func (s *Server) addCdpClient(c *cdpClient) {
	s.cdpMu.Lock()
	defer s.cdpMu.Unlock()
	if s.cdpClients == nil {
		s.cdpClients = make(map[*cdpClient]struct{})
	}
	s.cdpClients[c] = struct{}{}
}

func (s *Server) removeCdpClient(c *cdpClient) {
	s.cdpMu.Lock()
	defer s.cdpMu.Unlock()
	delete(s.cdpClients, c)
}

// isCdpEvent reports whether a response that matches no pending command is a
// relayed debugger event.
func isCdpEvent(resp *Response) bool {
	return resp.Type == CdpEventType
}

// fanoutCdpEvent pushes one debugger event to every listening relay client.
// Events are the whole reason this endpoint is a socket rather than another
// HTTP relay call: console messages, network responses and trace chunks all
// arrive unasked-for, and an instrument that only saw command replies would
// see nothing at all.
func (s *Server) fanoutCdpEvent(origin *extConn, resp *Response) {
	s.cdpMu.Lock()
	clients := make([]*cdpClient, 0, len(s.cdpClients))
	for c := range s.cdpClients {
		// A client pinned to another browser must not see this one's
		// events: tab 5 here is not its tab 5.
		if origin != nil && c.target.Instance != "" && c.target.Instance != origin.id {
			continue
		}
		clients = append(clients, c)
	}
	s.cdpMu.Unlock()

	if len(clients) == 0 {
		return
	}
	// Marshalled once, queued per client, and never written from here: this
	// runs on the extension read loop, and dispatch's invariant is that
	// nothing on that loop may wait on a receiver. It used to write each
	// client's socket synchronously, so one consumer that stopped reading
	// stopped the whole bridge — captures, command replies and every other
	// client with it.
	frame, err := json.Marshal(map[string]any{"type": CdpEventType, "data": resp.Data})
	if err != nil {
		s.logger.Debug().Err(err).Msg("cdp event marshal failed")
		return
	}
	for _, c := range clients {
		if c.queueEvent(frame) {
			continue
		}
		// Backlog full. The choice here is between dropping events and
		// dropping the client, and it goes to the client: every instrument
		// on the far side (HAR, trace, console, coverage) reconstructs
		// state from a complete event stream, so a silent hole in it does
		// not degrade the answer, it makes a wrong answer look right. A
		// disconnect is the one outcome the client cannot mistake for a
		// quiet page, and it can reconnect and re-attach to resync.
		s.logger.Warn().Msg("cdp relay client fell behind; disconnecting it")
		c.drop("cdp relay: event backlog overflowed, stream truncated")
	}
}

// handleCdpSocket serves /monoagent/cdp.
func (s *Server) handleCdpSocket(w http.ResponseWriter, r *http.Request) {
	// Compared before the upgrade so a rejected client gets a plain 401 it
	// can read, rather than a WebSocket that closes for no stated reason.
	if s.token == "" ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Before the upgrade: a refused client reads a plain 503, not a socket that
	// closes for no stated reason.
	if accountRefuses() { // the upgrade
		http.Error(w, accountdoor.Message, http.StatusServiceUnavailable)
		return
	}
	target := targetFromQuery(r.URL.Query())
	if c, err := s.resolve(target); err == nil {
		target = Target{Instance: c.id, Profile: target.Profile}
	} else if !errors.Is(err, ErrNoExtension) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error().Err(err).Msg("cdp relay upgrade failed")
		return
	}
	conn.SetReadLimit(maxMessageSize)

	client := newCdpClient(conn)
	client.target = target
	s.addCdpClient(client)
	go client.writeEvents()
	s.logger.Debug().Msg("cdp relay client connected")

	defer func() {
		s.removeCdpClient(client)
		client.drop("") // stops writeEvents and closes the socket
		_ = conn.Close()
		// Detach whatever this client left attached. Chrome shows the user
		// a banner for every attached debuggee; a CLI that crashed mid-run
		// must not leave one on their tab until the extension's idle sweep
		// notices 30 seconds later.
		for _, tabID := range client.attachedTabs() {
			go func(id int) {
				_, err := s.SendCommandTo(client.target, &Command{Type: CmdCdpDetach, TabID: id}, cdpCommandTimeout)
				if err != nil {
					s.logger.Debug().Err(err).Int("tabId", id).Msg("cdp detach on disconnect failed")
				}
			}(tabID)
		}
		s.logger.Debug().Msg("cdp relay client disconnected")
	}()

	inflight := make(chan struct{}, maxInflightCdpCommands)
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var cmd Command
		if err := json.Unmarshal(msg, &cmd); err != nil {
			// Never log the payload: a CDP command can carry page content.
			s.logger.Debug().Int("len", len(msg)).Msg("invalid cdp envelope")
			continue
		}
		if !isCdpCommand(cmd.Type) {
			_ = client.write(&Response{
				ID:    cmd.ID,
				Error: fmt.Sprintf("the CDP relay carries %s/%s/%s only, not %q", CmdCdp, CmdCdpAttach, CmdCdpDetach, cmd.Type),
			})
			continue
		}
		if accountRefuses() { // each command
			// A socket opened before the lock is refused command by command, not
			// cut: what is in flight finishes, and it works again once a login lands.
			_ = client.write(&Response{ID: cmd.ID, Type: cmd.Type, Error: accountdoor.Message})
			continue
		}

		select {
		case inflight <- struct{}{}:
		default:
			_ = client.write(&Response{ID: cmd.ID, Error: "cdp relay busy: too many commands in flight"})
			continue
		}
		go func(cmd Command) {
			defer func() { <-inflight }()
			s.relayCdpCommand(client, &cmd)
		}(cmd)
	}
}

func isCdpCommand(t string) bool {
	return t == CmdCdp || t == CmdCdpAttach || t == CmdCdpDetach
}

// relayCdpCommand sends one envelope to the extension and writes the answer
// back to the client that asked, keeping the client's id so the bridge
// transport can match it.
func (s *Server) relayCdpCommand(client *cdpClient, cmd *Command) {
	clientID := cmd.ID
	// The extension's own ids must be unique across this process, and a
	// client's are only unique to itself. SendCommand mints one when empty.
	cmd.ID = ""

	resp, err := s.SendCommandTo(client.target, cmd, cdpCommandTimeout)
	if resp == nil {
		resp = &Response{}
	}
	if err != nil && resp.Error == "" {
		resp.Error = err.Error()
	}
	resp.ID = clientID
	resp.Type = cmd.Type

	if cmd.Type == CmdCdpAttach && resp.Success {
		// The extension reports which tab it actually attached to, which is
		// the only way the client learns the id when it asked for "whatever
		// is active" — and the only record this side has for detaching it.
		client.rememberTab(attachedTabID(cmd, resp))
	}
	if cmd.Type == CmdCdpDetach {
		client.forgetTab(cmd.TabID)
	}

	if err := client.write(resp); err != nil {
		s.logger.Debug().Err(err).Msg("cdp reply write failed")
	}
}

// attachedTabID prefers the tab the extension reports over the one asked
// for, since an attach with no tabId resolves to the active tab.
func attachedTabID(cmd *Command, resp *Response) int {
	if data, ok := resp.Data.(map[string]interface{}); ok {
		if raw, ok := data["tabId"].(float64); ok && raw != 0 {
			return int(raw)
		}
	}
	return cmd.TabID
}
