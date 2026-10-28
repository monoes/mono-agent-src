package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// syncBuffer wraps bytes.Buffer with a mutex: the test goroutine reads it
// via String() while the server's readLoop goroutine concurrently writes to
// it through the zerolog logger, which a plain bytes.Buffer doesn't allow
// (the race detector catches this as a real data race, not just a
// theoretical one — it fires reliably under `go test -race`).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestResolveListenAddr_EnvOverride(t *testing.T) {
	t.Setenv(ExtensionPortEnv, "9500")
	got, err := resolveListenAddr("127.0.0.1:9222")
	if err != nil {
		t.Fatalf("resolveListenAddr: %v", err)
	}
	if got != "127.0.0.1:9500" {
		t.Errorf("resolveListenAddr = %q, want 127.0.0.1:9500", got)
	}
}

func TestResolveListenAddr_EnvOverrideInvalid(t *testing.T) {
	for _, val := range []string{"abc", "0", "-1", "70000", "9222 extra"} {
		t.Setenv(ExtensionPortEnv, val)
		if _, err := resolveListenAddr("127.0.0.1:9222"); err == nil {
			t.Errorf("resolveListenAddr with %s=%q should fail, got nil", ExtensionPortEnv, val)
		}
	}
}

func TestResolveListenAddr_NoEnv(t *testing.T) {
	t.Setenv(ExtensionPortEnv, "")
	got, err := resolveListenAddr("127.0.0.1:9222")
	if err != nil {
		t.Fatalf("resolveListenAddr: %v", err)
	}
	if got != "127.0.0.1:9222" {
		t.Errorf("resolveListenAddr = %q, want unchanged address", got)
	}
}

func TestListenCandidates_DefaultPortHasFallback(t *testing.T) {
	t.Setenv(ExtensionPortEnv, "")
	cands, err := listenCandidates("127.0.0.1:9222")
	if err != nil {
		t.Fatalf("listenCandidates: %v", err)
	}
	if len(cands) != 2 || cands[0] != "127.0.0.1:9222" || cands[1] != "127.0.0.1:9323" {
		t.Errorf("listenCandidates(9222) = %v, want [127.0.0.1:9222 127.0.0.1:9323]", cands)
	}
}

func TestListenCandidates_EnvOverrideDisablesFallback(t *testing.T) {
	t.Setenv(ExtensionPortEnv, "9500")
	cands, err := listenCandidates("127.0.0.1:9222")
	if err != nil {
		t.Fatalf("listenCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0] != "127.0.0.1:9500" {
		t.Errorf("listenCandidates with env override = %v, want [127.0.0.1:9500] only", cands)
	}
}

func TestListenCandidates_NonDefaultPortNoFallback(t *testing.T) {
	t.Setenv(ExtensionPortEnv, "")
	cands, err := listenCandidates("127.0.0.1:9999")
	if err != nil {
		t.Fatalf("listenCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0] != "127.0.0.1:9999" {
		t.Errorf("listenCandidates(9999) = %v, want [127.0.0.1:9999] only", cands)
	}
}

// freePort reserves an ephemeral port and releases it. The tiny race between
// release and reuse is standard test practice.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestTryListen_FallsBackOnBusyPort verifies the EADDRINUSE path: when the
// primary candidate is held, the next candidate is bound and reported.
func TestTryListen_FallsBackOnBusyPort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows lets a second listener share a bound port (no exclusive bind by default), so the port is never busy")
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold busy port: %v", err)
	}
	defer busy.Close()
	free := freePort(t)

	l, bound, err := tryListen([]string{
		"127.0.0.1:" + strconv.Itoa(busy.Addr().(*net.TCPAddr).Port),
		"127.0.0.1:" + strconv.Itoa(free),
	})
	if err != nil {
		t.Fatalf("tryListen: %v", err)
	}
	defer l.Close()
	want := "127.0.0.1:" + strconv.Itoa(free)
	if bound != want {
		t.Errorf("bound = %q, want %q", bound, want)
	}
}

// TestTryListen_AllBusyFails verifies both candidates busy surfaces an error
// instead of silently succeeding.
func TestTryListen_AllBusyFails(t *testing.T) {
	b1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port 1: %v", err)
	}
	defer b1.Close()
	b2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port 2: %v", err)
	}
	defer b2.Close()

	l, _, err := tryListen([]string{
		"127.0.0.1:" + strconv.Itoa(b1.Addr().(*net.TCPAddr).Port),
		"127.0.0.1:" + strconv.Itoa(b2.Addr().(*net.TCPAddr).Port),
	})
	if err == nil {
		l.Close()
		t.Fatal("tryListen with both ports busy should fail, got a listener")
	}
}

// TestStart_EnvOverrideBindsEnvPort drives the full Start path with an env
// override: the server must serve health on the env port (never touching
// the possibly-occupied default 9222 of the host running the tests).
func TestStart_EnvOverrideBindsEnvPort(t *testing.T) {
	// Isolate the home dir so the token write never touches the real
	// ~/.monoagent state (withTempHome convention from token_test.go).
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	port := freePort(t)
	t.Setenv(ExtensionPortEnv, strconv.Itoa(port))

	srv := NewServer("127.0.0.1:9222", zerolog.Nop())
	errCh := srv.StartAsync(context.Background())
	defer srv.Close() //nolint:errcheck

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for !Probe(base) {
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy at %s", base)
		}
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case err := <-errCh:
		t.Fatalf("Start errored: %v", err)
	default:
	}
}

// TestReadLoop_NeverLogsRawPayload drives a real extension response
// (well-formed and malformed) containing a sentinel cookie value through the
// server end-to-end and asserts the sentinel never reaches the logger at any
// level — the raw-payload debug log this guards against (MA-03) would leak
// live session cookies from get_cookies/eval responses into log files.
func TestReadLoop_NeverLogsRawPayload(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	const sentinel = "sentinel-cookie-value-1a2b3c4d"

	logBuf := &syncBuffer{}
	logger := zerolog.New(logBuf).Level(zerolog.TraceLevel)

	port := freePort(t)
	t.Setenv(ExtensionPortEnv, strconv.Itoa(port))

	srv := NewServer("127.0.0.1:9222", logger)
	errCh := srv.StartAsync(context.Background())
	defer srv.Close() //nolint:errcheck

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for !Probe(base) {
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy at %s", base)
		}
		time.Sleep(50 * time.Millisecond)
	}

	wsURL := "ws://127.0.0.1:" + strconv.Itoa(port) + "/monoagent"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial extension endpoint: %v", err)
	}
	defer conn.Close()
	authenticateTestConn(t, conn)

	// Well-formed response carrying the sentinel in Data (as get_cookies
	// would return a real cookie value).
	wellFormed, err := json.Marshal(Response{
		ID:      "req-1",
		Success: true,
		Data:    map[string]interface{}{"cookies": []string{sentinel}},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, wellFormed); err != nil {
		t.Fatalf("write well-formed response: %v", err)
	}

	// Malformed JSON that still embeds the sentinel, exercising the
	// invalid-response logging path.
	malformed := []byte(`{"id":"req-2","data":"` + sentinel + `"`) // missing closing brace
	if err := conn.WriteMessage(websocket.TextMessage, malformed); err != nil {
		t.Fatalf("write malformed response: %v", err)
	}

	// Give readLoop time to process and log both messages.
	time.Sleep(200 * time.Millisecond)

	if strings.Contains(logBuf.String(), sentinel) {
		t.Fatalf("sentinel leaked into logs:\n%s", logBuf.String())
	}

	select {
	case err := <-errCh:
		t.Fatalf("Start errored: %v", err)
	default:
	}
}

// authenticateTestConn sends a valid auth frame using the current extension
// token (as read from the same isolated HOME the server under test wrote
// it to) so callers can proceed to exercise the post-auth protocol.
func authenticateTestConn(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	tok, err := CurrentToken()
	if err != nil {
		t.Fatalf("read extension token: %v", err)
	}
	authenticateTestConnWith(t, conn, tok)
}

// authenticateTestConnWith sends an auth frame carrying an arbitrary
// (possibly wrong) value, for exercising the auth-rejection paths.
func authenticateTestConnWith(t *testing.T, conn *websocket.Conn, tok string) {
	t.Helper()
	frame, err := json.Marshal(authFrame{Type: "auth", Token: tok})
	if err != nil {
		t.Fatalf("marshal auth frame: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
}

// startAuthTestServer starts a server on an isolated HOME/port and returns
// its base WS URL, for the auth-handshake tests below.
func startAuthTestServer(t *testing.T) string {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	port := freePort(t)
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
		time.Sleep(50 * time.Millisecond)
	}
	return "ws://127.0.0.1:" + strconv.Itoa(port) + "/monoagent"
}

// TestHandleWS_NoAuthFrameRejected covers MA-01: a socket that never sends
// an auth frame must be closed, not treated as the extension.
func TestHandleWS_NoAuthFrameRejected(t *testing.T) {
	wsURL := startAuthTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(authTimeout + 2*time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed for missing auth frame")
	}
}

// TestHandleWS_WrongTokenRejected covers MA-01: a socket presenting the
// wrong token is closed with the unauthorized close code.
func TestHandleWS_WrongTokenRejected(t *testing.T) {
	wsURL := startAuthTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	bad := "x"
	authenticateTestConnWith(t, conn, bad)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected a websocket close error, got %v", err)
	}
	if closeErr.Code != unauthorizedCloseCode {
		t.Errorf("close code = %d, want %d", closeErr.Code, unauthorizedCloseCode)
	}
}

// TestHandleWS_CorrectTokenAccepted covers MA-01: a socket presenting the
// correct token becomes the active extension connection.
func TestHandleWS_CorrectTokenAccepted(t *testing.T) {
	wsURL := startAuthTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	authenticateTestConn(t, conn)

	// A ping frame should now be delivered on the (now-treated-as-extension)
	// socket within the ping interval-ish window, proving handleWS reached
	// the post-auth path rather than closing the connection.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, _, err := conn.ReadMessage()
	if err != nil {
		// A read timeout here (rather than a close) still proves the
		// connection was accepted and kept open; only a close error is a
		// real failure.
		if _, ok := err.(*websocket.CloseError); ok {
			t.Fatalf("connection closed after valid auth: %v", err)
		}
	} else if msgType != websocket.TextMessage && msgType != websocket.PingMessage {
		t.Errorf("unexpected message type after auth: %d", msgType)
	}
}

// TestHandleWS_UnauthenticatedSocketCannotReplaceAuthenticated covers MA-01's
// explicit acceptance criterion: a second, unauthenticated connection must
// not be able to knock out an already-authenticated extension connection.
func TestHandleWS_UnauthenticatedSocketCannotReplaceAuthenticated(t *testing.T) {
	wsURL := startAuthTestServer(t)

	good, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial (authenticated): %v", err)
	}
	defer good.Close()
	authenticateTestConn(t, good)

	// Give handleWS time to install `good` as s.conn.
	time.Sleep(100 * time.Millisecond)

	bad, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial (impostor): %v", err)
	}
	defer bad.Close()
	impostor := "x"
	authenticateTestConnWith(t, bad, impostor)

	// The impostor must be rejected...
	bad.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := bad.ReadMessage(); err == nil {
		t.Fatal("expected impostor connection to be closed")
	}

	// ...and the authenticated connection must still be alive: a write
	// through it should still succeed.
	good.SetWriteDeadline(time.Now().Add(2 * time.Second))
	resp, _ := json.Marshal(Response{ID: "still-alive"})
	if err := good.WriteMessage(websocket.TextMessage, resp); err != nil {
		t.Errorf("authenticated connection was replaced/closed: %v", err)
	}
}

// TestHandleWS_OversizedMessageClosesConnection covers MA-14: a frame past
// maxMessageSize must close the connection (gorilla/websocket applies no
// size limit by default) rather than let the server buffer it unbounded.
func TestHandleWS_OversizedMessageClosesConnection(t *testing.T) {
	wsURL := startAuthTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	authenticateTestConn(t, conn)

	oversized := make([]byte, maxMessageSize+1)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, oversized); err != nil {
		// A write-side error here (broken pipe) is also an acceptable
		// signal that the server closed the connection.
		return
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection to be closed after an oversized message")
	}
}

// TestReadLoop_MalformedFrameDoesNotCrashLoop covers MA-14: a malformed
// frame on an already-authenticated connection must be logged and skipped,
// not tear down the whole readLoop — a well-formed frame sent right after
// it must still be processed normally.
func TestReadLoop_MalformedFrameDoesNotCrashLoop(t *testing.T) {
	wsURL := startAuthTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	authenticateTestConn(t, conn)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{not valid json`)); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}

	// The connection must still be alive and processing: a well-formed
	// frame sent right after should not error either.
	time.Sleep(100 * time.Millisecond)
	wellFormed, _ := json.Marshal(Response{ID: "after-malformed", Success: true})
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, wellFormed); err != nil {
		t.Fatalf("connection did not survive a malformed frame: %v", err)
	}
}
