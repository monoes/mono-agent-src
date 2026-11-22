package extension

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

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
	timeout := relayTimeout(r.URL.Query().Get("timeout_ms"))
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
		close(c.gone)
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

// relayTimeout reads the relay's timeout_ms parameter.
func relayTimeout(ms string) time.Duration {
	if ms == "" {
		return defaultTimeout
	}
	n, err := time.ParseDuration(ms + "ms")
	if err != nil || n <= 0 || n > 10*time.Minute {
		return defaultTimeout
	}
	return n
}
