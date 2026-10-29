package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// freePortTB and authenticateTB mirror server_test.go's helpers, widened to
// testing.TB so the benchmark at the bottom of this file can use the same rig
// the tests do.
func freePortTB(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func authenticateTB(t testing.TB, conn *websocket.Conn) {
	t.Helper()
	tok, err := CurrentToken()
	if err != nil {
		t.Fatalf("read extension token: %v", err)
	}
	frame, err := json.Marshal(authFrame{Type: "auth", Token: tok})
	if err != nil {
		t.Fatalf("marshal auth frame: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
}

// cdpTestRig is a running server with a fake extension attached, plus the
// pieces a test needs to drive both ends of the CDP relay.
type cdpTestRig struct {
	port int
	srv  *Server
	ext  *websocket.Conn // the fake extension's socket
}

func startCdpRig(t testing.TB) *cdpTestRig {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	port := freePortTB(t)
	t.Setenv(ExtensionPortEnv, strconv.Itoa(port))

	srv := NewServer("127.0.0.1:9222", zerolog.Nop())
	srv.StartAsync(context.Background())
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for !Probe(base) {
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy at %s", base)
		}
		time.Sleep(20 * time.Millisecond)
	}

	ext, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/monoagent", nil)
	if err != nil {
		t.Fatalf("dial extension endpoint: %v", err)
	}
	t.Cleanup(func() { ext.Close() })
	authenticateTB(t, ext)

	// Wait for the server to install the socket, so a command written
	// immediately after this helper returns has somewhere to go.
	deadline = time.Now().Add(2 * time.Second)
	for !srv.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatal("server never registered the extension connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &cdpTestRig{port: port, srv: srv, ext: ext}
}

// dialCdp opens the CDP relay socket the way monobrowse's bridge transport
// does: a plain WebSocket carrying the relay token as a header.
func (r *cdpTestRig) dialCdp(t testing.TB, token string) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set(tokenHeader, token)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(
		"ws://127.0.0.1:"+strconv.Itoa(r.port)+"/monoagent/cdp", h)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial cdp relay: %v (status %d)", err, resp.StatusCode)
		}
		t.Fatalf("dial cdp relay: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func (r *cdpTestRig) token(t testing.TB) string {
	t.Helper()
	if r.srv.token == "" {
		t.Fatal("server has no relay token")
	}
	return r.srv.token
}

// answerExtension plays the extension: reads one command, checks it, and
// writes back the given response data. Returns the command it saw.
func (r *cdpTestRig) answerExtension(t testing.TB, data any, errMsg string) *Command {
	t.Helper()
	r.ext.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msg, err := r.ext.ReadMessage()
		if err != nil {
			t.Fatalf("extension read: %v", err)
		}
		var cmd Command
		if err := json.Unmarshal(msg, &cmd); err != nil || cmd.Type == "" {
			continue // a ping or other frame; keep looking
		}
		resp := Response{ID: cmd.ID, Success: errMsg == "", Data: data, Error: errMsg}
		out, _ := json.Marshal(resp)
		if err := r.ext.WriteMessage(websocket.TextMessage, out); err != nil {
			t.Fatalf("extension write: %v", err)
		}
		return &cmd
	}
}

func readJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(msg, v); err != nil {
		t.Fatalf("decode %q: %v", string(msg), err)
	}
}

// TestCdpRelay_ForwardsCommandAndResponse is the round trip the bridge
// transport depends on: a cdp envelope in, a chrome.debugger result out.
func TestCdpRelay_ForwardsCommandAndResponse(t *testing.T) {
	rig := startCdpRig(t)
	conn := rig.dialCdp(t, rig.token(t))

	env := map[string]any{
		"id":    "cdp-1",
		"type":  CmdCdp,
		"tabId": 42,
		"params": map[string]any{
			"method": "Page.navigate",
			"params": map[string]any{"url": "https://example.test"},
		},
	}
	if err := conn.WriteJSON(env); err != nil {
		t.Fatalf("write envelope: %v", err)
	}

	cmd := rig.answerExtension(t, map[string]any{"result": map[string]any{"frameId": "F1"}}, "")
	if cmd.Type != CmdCdp {
		t.Errorf("extension saw type %q, want %q", cmd.Type, CmdCdp)
	}
	if cmd.TabID != 42 {
		t.Errorf("extension saw tabId %d, want 42", cmd.TabID)
	}
	if got := cmd.Params["method"]; got != "Page.navigate" {
		t.Errorf("extension saw method %v, want Page.navigate", got)
	}

	var reply struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
		Data    struct {
			Result map[string]any `json:"result"`
		} `json:"data"`
	}
	readJSON(t, conn, &reply)
	if reply.ID != "cdp-1" || !reply.Success {
		t.Fatalf("reply = %+v, want id cdp-1 success", reply)
	}
	if reply.Data.Result["frameId"] != "F1" {
		t.Errorf("result = %v, want frameId F1", reply.Data.Result)
	}
}

// TestCdpRelay_ReportsExtensionError keeps a failed CDP command a failure
// the caller can read, rather than a timeout.
func TestCdpRelay_ReportsExtensionError(t *testing.T) {
	rig := startCdpRig(t)
	conn := rig.dialCdp(t, rig.token(t))

	_ = conn.WriteJSON(map[string]any{
		"id": "cdp-2", "type": CmdCdp, "tabId": 42,
		"params": map[string]any{"method": "HeapProfiler.enable"},
	})
	rig.answerExtension(t, nil, "Not allowed")

	var reply struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	readJSON(t, conn, &reply)
	if reply.Success || reply.Error != "Not allowed" {
		t.Fatalf("reply = %+v, want failure carrying the extension's message", reply)
	}
}

// TestCdpRelay_FansOutEvents is the direction the instruments live on:
// chrome.debugger events arrive unasked-for and must reach every listening
// CDP client, not be dropped as an unmatched response.
func TestCdpRelay_FansOutEvents(t *testing.T) {
	rig := startCdpRig(t)
	a := rig.dialCdp(t, rig.token(t))
	b := rig.dialCdp(t, rig.token(t))
	// Both sockets must be registered before the event is pushed.
	time.Sleep(100 * time.Millisecond)

	event, _ := json.Marshal(Response{
		Type:    CdpEventType,
		Success: true,
		Data: map[string]any{
			"tabId":  42,
			"method": "Network.responseReceived",
			"params": map[string]any{"requestId": "R1"},
		},
	})
	if err := rig.ext.WriteMessage(websocket.TextMessage, event); err != nil {
		t.Fatalf("extension write: %v", err)
	}

	for i, conn := range []*websocket.Conn{a, b} {
		var got struct {
			Type string `json:"type"`
			Data struct {
				TabID  int    `json:"tabId"`
				Method string `json:"method"`
			} `json:"data"`
		}
		readJSON(t, conn, &got)
		if got.Type != CdpEventType || got.Data.Method != "Network.responseReceived" {
			t.Errorf("client %d got %+v", i, got)
		}
	}
}

// TestCdpRelay_RejectsWrongToken: the relay reaches a browser the user is
// logged into, so it is token-gated like /monoagent/relay.
func TestCdpRelay_RejectsWrongToken(t *testing.T) {
	rig := startCdpRig(t)
	h := http.Header{}
	h.Set(tokenHeader, "not-the-token")
	conn, resp, err := websocket.DefaultDialer.Dial(
		"ws://127.0.0.1:"+strconv.Itoa(rig.port)+"/monoagent/cdp", h)
	if err == nil {
		conn.Close()
		t.Fatal("expected the relay to refuse a wrong token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// TestCdpRelay_RefusesNonCdpCommands: this socket exists to carry CDP. It
// must not become a second, unaudited path for every extension command.
func TestCdpRelay_RefusesNonCdpCommands(t *testing.T) {
	rig := startCdpRig(t)
	conn := rig.dialCdp(t, rig.token(t))

	_ = conn.WriteJSON(map[string]any{"id": "x-1", "type": CmdEval, "tabId": 42,
		"params": map[string]any{"expression": "document.cookie"}})

	var reply struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	readJSON(t, conn, &reply)
	if reply.Success {
		t.Fatal("the CDP relay accepted a non-CDP command")
	}
	if reply.ID != "x-1" || reply.Error == "" {
		t.Errorf("reply = %+v, want a refusal naming the command", reply)
	}
}

// TestCdpRelay_DetachesOnClientDisconnect: a client that vanishes must not
// leave Chrome's debugging banner on the user's tab forever.
func TestCdpRelay_DetachesOnClientDisconnect(t *testing.T) {
	rig := startCdpRig(t)
	conn := rig.dialCdp(t, rig.token(t))

	_ = conn.WriteJSON(map[string]any{"id": "cdp-3", "type": CmdCdpAttach, "tabId": 42})
	rig.answerExtension(t, map[string]any{"tabId": 42}, "")
	var reply struct {
		Success bool `json:"success"`
	}
	readJSON(t, conn, &reply)
	if !reply.Success {
		t.Fatal("attach was refused")
	}

	conn.Close()

	cmd := rig.answerExtension(t, nil, "")
	if cmd.Type != CmdCdpDetach || cmd.TabID != 42 {
		t.Errorf("after disconnect the extension saw %s/%d, want %s for tab 42",
			cmd.Type, cmd.TabID, CmdCdpDetach)
	}
}

// autoAnswer plays the extension for a benchmark: it answers every command
// with a result of the requested size, on its own goroutine.
func (r *cdpTestRig) autoAnswer(t testing.TB, payloadBytes int, stop <-chan struct{}) {
	t.Helper()
	payload := strings.Repeat("x", payloadBytes)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			r.ext.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, msg, err := r.ext.ReadMessage()
			if err != nil {
				return
			}
			var cmd Command
			if json.Unmarshal(msg, &cmd) != nil || cmd.Type == "" {
				continue
			}
			out, _ := json.Marshal(Response{
				ID: cmd.ID, Success: true,
				Data: map[string]any{"result": map[string]any{"data": payload}},
			})
			if r.ext.WriteMessage(websocket.TextMessage, out) != nil {
				return
			}
		}
	}()
}

// BenchmarkCdpRelay measures what the bridge adds over a direct CDP socket:
// one extra process hop each way (CDP client → this server → extension), with
// a real extension's chrome.debugger call and an MV3 service-worker wake-up
// still on top of whatever this reports.
//
//	go test ./internal/extension/ -run '^$' -bench CdpRelay -benchmem
func BenchmarkCdpRelay(b *testing.B) {
	for _, size := range []int{0, 64 << 10, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("payload=%dB", size), func(b *testing.B) {
			rig := startCdpRig(b)
			conn := rig.dialCdp(b, rig.token(b))
			stop := make(chan struct{})
			defer close(stop)
			rig.autoAnswer(b, size, stop)

			env := map[string]any{
				"id": "cdp-1", "type": CmdCdp, "tabId": 42,
				"params": map[string]any{"method": "Page.captureScreenshot"},
			}
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := conn.WriteJSON(env); err != nil {
					b.Fatalf("write: %v", err)
				}
				conn.SetReadDeadline(time.Now().Add(30 * time.Second))
				if _, _, err := conn.ReadMessage(); err != nil {
					b.Fatalf("read: %v", err)
				}
			}
		})
	}
}
