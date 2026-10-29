package extension

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// startStatusTestServer brings up a real Server on a free port and returns
// it alongside the HTTP base URL, the extension WebSocket URL, and the
// buffer its logger writes to (so a test can assert on how an event was
// logged, not just that it happened).
func startStatusTestServer(t *testing.T) (*Server, string, string, *syncBuffer) {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	port := freePort(t)
	t.Setenv(ExtensionPortEnv, strconv.Itoa(port))

	logs := &syncBuffer{}
	srv := NewServer("127.0.0.1:9222", zerolog.New(logs))
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
	return srv, base, "ws://127.0.0.1:" + strconv.Itoa(port) + "/monoagent", logs
}

func fetchHealth(t *testing.T, base string) Status {
	t.Helper()
	resp, err := http.Get(base + "/monoagent/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
	var got Status
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return got
}

// dialAndAuthenticate opens an extension socket and completes the auth
// handshake with the real on-disk token.
func dialAndAuthenticate(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Also keeps conn reachable until the test ends: a socket nobody holds
	// is closed by its finalizer at the next GC, which the server sees as
	// the extension going away.
	t.Cleanup(func() { _ = conn.Close() })
	tok, err := CurrentToken()
	if err != nil {
		t.Fatalf("CurrentToken: %v", err)
	}
	if err := conn.WriteJSON(authFrame{Type: "auth", Token: tok}); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	return conn
}

// waitForConnected polls health until connected matches want.
func waitForConnected(t *testing.T, base string, want bool) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := fetchHealth(t, base)
		if got.Connected == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never reported connected=%v (last: %+v)", want, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHealth_IdentifiesItselfAndReportsWaiting covers the popup's first
// question: "is the thing on this port actually the bridge, and does it
// have an extension?". A Chrome holding 9222 for its own CDP answers that
// path with a 404, so the service marker is what tells them apart.
func TestHealth_IdentifiesItselfAndReportsWaiting(t *testing.T) {
	_, base, _, _ := startStatusTestServer(t)

	got := fetchHealth(t, base)
	if got.Service != ServiceName {
		t.Errorf("service = %q, want %q", got.Service, ServiceName)
	}
	if got.Status != StatusWaiting {
		t.Errorf("status = %q, want %q", got.Status, StatusWaiting)
	}
	if got.Connected {
		t.Error("connected = true with no extension attached")
	}
	if got.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", got.PID, os.Getpid())
	}
	if !strings.HasPrefix(got.WSURL, "ws://127.0.0.1:") || !strings.HasSuffix(got.WSURL, "/monoagent") {
		t.Errorf("wsUrl = %q, want the extension socket URL", got.WSURL)
	}
	if got.Addr == "" {
		t.Error("addr is empty; the popup needs to know which port answered")
	}
}

// TestHealth_ReportsConnectedWhileExtensionAttached covers the paired,
// working case — and that the legacy `connected` field still means what
// RemoteSender.IsConnected has always read it to mean.
func TestHealth_ReportsConnectedWhileExtensionAttached(t *testing.T) {
	_, base, wsURL, _ := startStatusTestServer(t)

	conn := dialAndAuthenticate(t, wsURL)
	defer conn.Close()

	got := waitForConnected(t, base, true)
	if got.Status != StatusConnected {
		t.Errorf("status = %q, want %q", got.Status, StatusConnected)
	}
	if !NewRemoteSender(base).IsConnected() {
		t.Error("RemoteSender.IsConnected = false while an extension is attached")
	}
}

// TestHealth_ReportsUnpairedAfterRejectedAuth is the distinction the popup
// could not draw before: a bridge that is running and reachable but is
// turning this extension away because it holds the wrong token.
func TestHealth_ReportsUnpairedAfterRejectedAuth(t *testing.T) {
	_, base, wsURL, _ := startStatusTestServer(t)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.WriteJSON(authFrame{Type: "auth", Token: "not-the-token"}); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		got := fetchHealth(t, base)
		if got.Status == StatusUnpaired {
			if got.Connected {
				t.Error("connected = true after a rejected handshake")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never became %q (last: %+v)", StatusUnpaired, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHealth_SurvivesDroppedExtensionSocket is requirement 3: an MV3
// service worker is killed when idle and respawns on an event, so the
// socket drops and comes back constantly. The server must keep serving,
// report the gap honestly, accept the reconnect — and not call any of it
// an error.
func TestHealth_SurvivesDroppedExtensionSocket(t *testing.T) {
	_, base, wsURL, logs := startStatusTestServer(t)

	first := dialAndAuthenticate(t, wsURL)
	waitForConnected(t, base, true)

	// Kill the socket the way a suspended service worker does: gorilla's
	// Close drops the TCP connection without sending a close frame, so the
	// server's read fails with an abnormal-closure (1006), exactly as it
	// does when Chrome tears down an idle worker.
	first.Close()

	got := waitForConnected(t, base, false)
	if got.Status != StatusWaiting {
		t.Errorf("status after a dropped socket = %q, want %q (a suspended service worker is not an unpaired one)", got.Status, StatusWaiting)
	}

	second := dialAndAuthenticate(t, wsURL)
	defer second.Close()
	if got := waitForConnected(t, base, true); got.Status != StatusConnected {
		t.Errorf("status after reconnect = %q, want %q", got.Status, StatusConnected)
	}

	if line := strings.ToLower(logs.String()); strings.Contains(line, `"level":"error"`) {
		t.Errorf("a service worker cycling its socket was logged as an error:\n%s", logs.String())
	}
}

// TestStatusOf_UnpairedWindowExpires keeps the unpaired signal honest: a
// rejected handshake from an hour ago says nothing about now.
func TestStatusOf_UnpairedWindowExpires(t *testing.T) {
	srv := NewServer("127.0.0.1:9222", zerolog.Nop())

	srv.noteAuthFailure()
	if got := srv.Status(); got.Status != StatusUnpaired {
		t.Fatalf("status right after a rejected handshake = %q, want %q", got.Status, StatusUnpaired)
	}

	srv.statusMu.Lock()
	srv.lastAuthFailure = time.Now().Add(-2 * unpairedWindow)
	srv.statusMu.Unlock()

	if got := srv.Status(); got.Status != StatusWaiting {
		t.Errorf("status long after a rejected handshake = %q, want %q", got.Status, StatusWaiting)
	}
}

// TestFetchStatus_RejectsNonBridgeListener covers the other thing on 9222:
// a Chrome started with --remote-debugging-port answers HTTP there, so
// "something replied" must not be read as "the bridge is up".
func TestFetchStatus_RejectsNonBridgeListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	srv := &http.Server{}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	if _, err = FetchStatus("http://" + ln.Addr().String()); err == nil {
		t.Error("FetchStatus accepted a listener that is not a monoagent bridge")
	}
}

func TestFetchStatus_ReadsARunningBridge(t *testing.T) {
	_, base, _, _ := startStatusTestServer(t)

	got, err := FetchStatus(base)
	if err != nil {
		t.Fatalf("FetchStatus: %v", err)
	}
	if got.Service != ServiceName || got.Status != StatusWaiting {
		t.Errorf("FetchStatus = %+v, want a waiting monoagent bridge", got)
	}
}

// TestHealth_ReportsInFlightWork answers the popup's "can I fire a capture
// right now, or is something already driving this tab?". The count covers
// work relayed in from another process too, since a relayed command goes
// through the same dispatch table.
func TestHealth_ReportsInFlightWork(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)

	conn := dialAndAuthenticate(t, wsURL)
	defer conn.Close()
	waitForConnected(t, base, true)

	if got := fetchHealth(t, base); got.InFlight != 0 {
		t.Fatalf("inFlight = %d on an idle bridge, want 0", got.InFlight)
	}

	// A command nobody answers: the extension side of this test never
	// replies, so it stays outstanding until it times out.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = srv.SendCommand(&Command{Type: CmdCreateTab}, 2*time.Second)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		got := fetchHealth(t, base)
		if got.InFlight == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inFlight never reported the outstanding command (last: %+v)", got)
		}
		time.Sleep(20 * time.Millisecond)
	}

	<-done
	if got := fetchHealth(t, base); got.InFlight != 0 {
		t.Errorf("inFlight = %d after the command settled, want 0", got.InFlight)
	}
}
