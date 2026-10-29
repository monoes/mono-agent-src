package extension

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// stallConn is a net.Conn that is stalled by construction: once stall() has been called, every Write
// blocks until Close and then fails, as a write to a peer that has stopped reading does. Reads block
// until Close. Nothing depends on the operating system's socket buffers or kernel autotuning.
type stallConn struct {
	mu      sync.Mutex
	stalled bool
	closed  chan struct{}
	once    sync.Once
	writing chan struct{} // receives once for each write that has started to block
}

func newStallConn() *stallConn {
	return &stallConn{closed: make(chan struct{}), writing: make(chan struct{}, 16)}
}

func (s *stallConn) stall() { s.mu.Lock(); s.stalled = true; s.mu.Unlock() }

func (s *stallConn) Write(p []byte) (int, error) {
	s.mu.Lock()
	stalled := s.stalled
	s.mu.Unlock()
	select {
	case <-s.closed:
		return 0, net.ErrClosed
	default:
	}
	if !stalled {
		return len(p), nil
	}
	select {
	case s.writing <- struct{}{}:
	default:
	}
	<-s.closed
	return 0, net.ErrClosed
}

func (s *stallConn) Read(p []byte) (int, error)       { <-s.closed; return 0, net.ErrClosed }
func (s *stallConn) Close() error                     { s.once.Do(func() { close(s.closed) }); return nil }
func (s *stallConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (s *stallConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (s *stallConn) SetDeadline(time.Time) error      { return nil }
func (s *stallConn) SetReadDeadline(time.Time) error  { return nil }
func (s *stallConn) SetWriteDeadline(time.Time) error { return nil }

// hijackWriter is a ResponseWriter whose Hijack hands out the fake connection, so that gorilla's
// Upgrader builds a real *websocket.Conn over it.
type hijackWriter struct {
	httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

// fakeBrowser installs a connected browser on srv, authenticated by the paired token tokenID and
// running the given profiles, whose socket is a stallConn. The handshake goes through; call
// conn.stall() to make every later write block.
func fakeBrowser(t *testing.T, srv *Server, tokenID, instance string, profiles ...string) (*extConn, *stallConn) {
	t.Helper()
	conn := newStallConn()
	req := httptest.NewRequest(http.MethodGet, "/monoagent", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Origin", "http://127.0.0.1")
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := up.Upgrade(&hijackWriter{conn: conn}, req, nil)
	if err != nil {
		t.Fatalf("upgrade over the fake connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := newExtConn(ws, authFrame{Type: "auth", Instance: instance, Version: "1.12.0", Profiles: profiles, tokenID: tokenID}, time.Now())
	if !srv.installConn(c) {
		t.Fatal("the fake browser was not installed")
	}
	go srv.readLoop(c) // as in handleWS: a closed socket ends the loop, which removes the browser
	return c, conn
}

// waitNoBrowsers polls until no browser is connected.
func waitNoBrowsers(t *testing.T, srv *Server, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for len(srv.Browsers()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the browser stayed connected: %+v", srv.Browsers())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var errStalledWriter = errors.New("the stalled writer was not released")

// A stalled writer holds the browser's write lock by construction. A policy change made meanwhile
// does not wait for it (the state lock is not held across a network write); the stalled command is
// bounded: its socket is closed after deliveryWriteTimeout, which is the fake's contract for
// releasing a blocked write, and the real-socket test below shows the same of a real one.
func TestAStalledWriteDoesNotHoldTheStateLockFakeConn(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	c, conn := fakeBrowser(t, srv, id, instA, "p-a")
	conn.stall()

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, 30*time.Second)
		errc <- err
	}()
	select {
	case <-conn.writing:
	case <-time.After(3 * time.Second):
		t.Fatal("the command never reached the stalled socket")
	}
	done := make(chan error, 1)
	go func() { _, err := SetAllowedProfiles(id, []string{"p-a", "p-b"}, true); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a policy change waited behind a stalled network write")
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("the stalled command reported success")
		}
	case <-time.After(deliveryWriteTimeout + 3*time.Second):
		t.Fatal(errStalledWriter)
	}
	waitNoBrowsers(t, srv, time.Second)
	_ = c
}

// A command that waits for the browser's writer behind a stalled reply or ping is bounded from the
// moment it is sent: it fails within about deliveryWriteTimeout, and closing the socket releases the
// reply that held the writer.
func TestACommandBehindAStalledReplyIsBoundedFakeConn(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	c, conn := fakeBrowser(t, srv, id, instA, "p-a")
	conn.stall()

	reply := make(chan error, 1)
	go func() { reply <- c.write([]byte(`{"id":"r1","success":true}`)) }()
	select {
	case <-conn.writing:
	case <-time.After(3 * time.Second):
		t.Fatal("the reply never reached the stalled socket")
	}

	start := time.Now()
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, 30*time.Second)
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a command behind a stalled reply reported success")
		}
		if d := time.Since(start); d > deliveryWriteTimeout+2*time.Second {
			t.Fatalf("the command waited %s behind the stalled reply", d)
		}
	case <-time.After(deliveryWriteTimeout + 4*time.Second):
		t.Fatal("a command waiting for the browser's writer was not bounded")
	}
	select {
	case err := <-reply:
		if err == nil {
			t.Fatal("the stalled reply reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal(errStalledWriter)
	}
	waitNoBrowsers(t, srv, time.Second)
}
