//go:build linux

package procbind

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	tGen     = "gen-test"
	tProfile = "p1"
	tScope   = "session:7"
	tToken   = "tok-1"
)

var tKey = bytes.Repeat([]byte{7}, 32)

type regMap struct {
	mu sync.Mutex
	m  map[string]Handle
}

func (r *regMap) Lookup(tokenID, profile, scope string) (Handle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.m[tokenID+"|"+profile+"|"+scope]
	return h, ok
}

func skipWithoutPeerPidfd(t *testing.T) {
	t.Helper()
	skipWithoutPidfd(t)
	if !PeerPidfdSupported() {
		t.Skip("SO_PEERPIDFD needs Linux 6.5; the binding service is unavailable on this kernel")
	}
}

// startServer listens on a short socket path and serves until the test ends.
func startServer(t *testing.T, reg Registry, tweak func(*Server)) (string, *Server) {
	t.Helper()
	skipWithoutPeerPidfd(t)
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "s")); _ = os.Remove(dir) })
	path := filepath.Join(dir, "s")
	if len(path) > 100 {
		t.Skipf("socket path too long: %s", path)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Profile: tProfile, Generation: tGen, Key: tKey, Registry: reg, Procs: lp(t), UID: os.Getuid(),
		Timeout: 3 * time.Second, Logf: func(f string, a ...any) { t.Logf("server: "+f, a...) }}
	if tweak != nil {
		tweak(s)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l) }()
	t.Cleanup(func() { _ = l.Close(); <-done })
	return path, s
}

func goodClient() Client {
	return Client{Key: tKey, Profile: tProfile, Generation: tGen, Timeout: 3 * time.Second}
}

func verifyOnce(t *testing.T, path string, c Client) (Verdict, error) {
	t.Helper()
	conn, err := DialUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return c.Verify(conn, tToken, tScope)
}

func register(t *testing.T, h Handle) *regMap {
	return &regMap{m: map[string]Handle{tToken + "|" + tProfile + "|" + tScope: h}}
}

func TestServiceAllowsTheWorkerItself(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), nil)
	v, err := verifyOnce(t, path, goodClient())
	if err != nil || v != VerdictAllow {
		t.Fatalf("the worker's own process: %v %v", v, err)
	}
}

// helperVerify is the verifier as a separate process: it prints RESULT=<verdict or error>.
func TestHelperVerify(t *testing.T) {
	if os.Getenv("PROCBIND_HELPER") != "1" {
		t.Skip("helper process only")
	}
	conn, err := DialUnix(os.Getenv("PROCBIND_SOCK"))
	if err != nil {
		fmt.Println("RESULT=dial:", err)
		return
	}
	defer conn.Close()
	v, err := goodClient().Verify(conn, tToken, tScope)
	if err != nil {
		fmt.Println("RESULT=error")
		return
	}
	fmt.Println("RESULT=" + string(v))
}

func runHelper(t *testing.T, sock string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperVerify$")
	cmd.Env = append(os.Environ(), "PROCBIND_HELPER=1", "PROCBIND_SOCK="+sock)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	return resultOf(t, string(out))
}

func resultOf(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "RESULT="); ok {
			return v
		}
	}
	t.Fatalf("helper printed no result:\n%s", out)
	return ""
}

// A verifier the registered worker started is a descendant of it; one the test started is not.
func TestServiceDescendantAllowedSiblingDenied(t *testing.T) {
	skipWithoutPeerPidfd(t)
	other := exec.Command("sleep", "60") // a live process the verifier is not under
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	path, _ := startServer(t, register(t, open(t, other.Process.Pid)), nil)
	if got := runHelper(t, path); got != "deny" {
		t.Errorf("a process the test started, outside the worker's tree: %q", got)
	}

	w := startShWorker(t)
	path2, _ := startServer(t, register(t, w.h), nil)
	if got := w.run(t, path2); got != "allow" {
		t.Errorf("a descendant of the registered worker: %q", got)
	}
}

type shWorker struct {
	h  Handle
	in io.Writer
}

// startShWorker starts a shell that runs the lines the test writes to its stdin.
func startShWorker(t *testing.T) shWorker {
	t.Helper()
	cmd := exec.Command("sh", "-c", `while read -r line; do eval "$line"; done`)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return shWorker{h: open(t, cmd.Process.Pid), in: in}
}

// run makes the shell start the helper verifier, so the verifier is the shell's child.
func (w shWorker) run(t *testing.T, sock string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	line := fmt.Sprintf("PROCBIND_HELPER=1 PROCBIND_SOCK=%q %q -test.run='^TestHelperVerify$' > %q 2>&1\n", sock, os.Args[0], out)
	if _, err := w.in.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		if b, err := os.ReadFile(out); err == nil && bytes.Contains(b, []byte("RESULT=")) {
			return resultOf(t, string(b))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("no helper result")
	return ""
}

func TestServiceDeniesAnUnregisteredToken(t *testing.T) {
	path, _ := startServer(t, &regMap{m: map[string]Handle{}}, nil)
	v, err := verifyOnce(t, path, goodClient())
	if err != nil || v != VerdictDeny {
		t.Fatalf("an unregistered token: %v %v", v, err)
	}
}

func TestClientRefusesAServiceThatCannotProveItself(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), nil)
	wrongKey := goodClient()
	wrongKey.Key = bytes.Repeat([]byte{8}, 32)
	if v, err := verifyOnce(t, path, wrongKey); !errors.Is(err, ErrNotAnswered) || !errors.Is(err, ErrAnswer) || v != VerdictDeny {
		t.Errorf("a different key must fail on the MAC: %v %v", v, err)
	}
	wrongGen := goodClient()
	wrongGen.Generation = "gen-other"
	if v, err := verifyOnce(t, path, wrongGen); !errors.Is(err, ErrNotAnswered) || v != VerdictDeny {
		t.Errorf("a different host generation: %v %v", v, err)
	}
	wrongProfile := goodClient()
	wrongProfile.Profile = "p2"
	if v, err := verifyOnce(t, path, wrongProfile); !errors.Is(err, ErrNotAnswered) || v != VerdictDeny {
		t.Errorf("a different profile: %v %v", v, err)
	}
}

// fakeService is a listener that is not the service: it replays a genuine allow it captured for an
// earlier request (an earlier verifier nonce) to a verifier that now uses a fresh one.
func TestClientRefusesAReplayedAnswer(t *testing.T) {
	skipWithoutPidfd(t)
	dir, _ := os.MkdirTemp("", "pb")
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "s")); _ = os.Remove(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ns := bytes.Repeat([]byte{3}, NonceLen)
	oldNV := bytes.Repeat([]byte{4}, NonceLen)
	genuine := Answer{NV: oldNV, NS: ns, Generation: tGen, TokenID: tToken, Profile: tProfile, Scope: tScope, Verdict: VerdictAllow}
	mac, _ := genuine.MAC(tKey)
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer c.Close()
		_ = writeFrame(c, wireHello{V: protoVersion, Generation: tGen, NS: hex.EncodeToString(ns)})
		var req wireRequest
		_ = readFrame(c, &req)
		_ = writeFrame(c, wireReply{V: protoVersion, Verdict: "allow", MAC: mac})
	}()
	if v, err := verifyOnce(t, path, goodClient()); !errors.Is(err, ErrNotAnswered) || !errors.Is(err, ErrAnswer) || v != VerdictDeny {
		t.Errorf("a replayed allow must be refused on its MAC: %v %v", v, err)
	}
}

func TestServiceRefusesAPeerOfAnotherUID(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), func(s *Server) { s.UID = os.Getuid() + 1 })
	if v, err := verifyOnce(t, path, goodClient()); !errors.Is(err, ErrNotAnswered) || v != VerdictDeny {
		t.Errorf("a peer of another uid was answered: %v %v", v, err)
	}
}

func fdCount(t *testing.T) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// rawRequest dials, reads the hello and returns the connection plus the service nonce.
func rawHello(t *testing.T, path string) (*net.UnixConn, wireHello) {
	t.Helper()
	c, err := DialUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	var h wireHello
	if err := readFrame(c, &h); err != nil {
		t.Fatal(err)
	}
	return c, h
}

func goodRequest(h wireHello) wireRequest {
	return wireRequest{V: protoVersion, TokenID: tToken, Profile: tProfile, Scope: tScope, NV: hex.EncodeToString(bytes.Repeat([]byte{9}, NonceLen)), NS: h.NS}
}

// expectClosedWithoutAnswer wants the service to end the connection (EOF) promptly, not answer
// and not stall until a deadline.
func expectClosedWithoutAnswer(t *testing.T, c *net.UnixConn, what string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	start := time.Now()
	var rep wireReply
	err := readFrame(c, &rep)
	if err == nil {
		t.Errorf("%s: the service answered: %+v", what, rep)
		return
	}
	// a close with unread request bytes left is reported by the kernel as a reset
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("%s: the connection was not closed by the service: %v", what, err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Errorf("%s: the service took %v to close", what, time.Since(start))
	}
}

func TestServiceRefusesBadRequestsWithoutAnswering(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), nil)
	mutate := map[string]func(r *wireRequest){
		"wrong version":        func(r *wireRequest) { r.V = 2 },
		"echoed nonce wrong":   func(r *wireRequest) { r.NS = hex.EncodeToString(bytes.Repeat([]byte{1}, NonceLen)) },
		"other profile":        func(r *wireRequest) { r.Profile = "p2" },
		"no token":             func(r *wireRequest) { r.TokenID = "" },
		"short verifier nonce": func(r *wireRequest) { r.NV = "abcd" },
		"non-hex nonce":        func(r *wireRequest) { r.NV = strings.Repeat("zz", NonceLen) },
		"empty request":        func(r *wireRequest) { *r = wireRequest{} },
	}
	for name, m := range mutate {
		c, h := rawHello(t, path)
		req := goodRequest(h)
		m(&req)
		if err := writeFrame(c, req); err != nil {
			t.Fatal(err)
		}
		expectClosedWithoutAnswer(t, c, name)
	}
	// raw garbage and an oversized length
	for name, raw := range map[string][]byte{
		"oversized length": binary.BigEndian.AppendUint32(nil, maxFrame+1),
		"garbage":          []byte("GET / HTTP/1.1\r\n\r\n"),
	} {
		c, _ := rawHello(t, path)
		_, _ = c.Write(raw)
		expectClosedWithoutAnswer(t, c, name)
	}
}

// settledFDs is the descriptor count once the service has finished with every connection so far.
func settledFDs(t *testing.T) int {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	return fdCount(t)
}

func TestServiceClosesADescriptorPassedWithTheRequest(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), nil)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// a refused request without a descriptor, to settle the count the same way
	c0, h0 := rawHello(t, path)
	bad := goodRequest(h0)
	bad.V = 2
	_ = writeFrame(c0, bad)
	expectClosedWithoutAnswer(t, c0, "baseline")
	_ = c0.Close()
	before := settledFDs(t)

	c, h := rawHello(t, path)
	var b bytes.Buffer
	_ = writeFrame(&b, goodRequest(h))
	if _, _, err := c.WriteMsgUnix(b.Bytes(), syscall.UnixRights(int(r.Fd())), nil); err != nil {
		t.Fatal(err)
	}
	expectClosedWithoutAnswer(t, c, "a request carrying a descriptor")
	_ = c.Close()
	if after := settledFDs(t); after > before {
		t.Errorf("descriptors open: %d before, %d after the request: a passed descriptor was kept", before, after)
	}
}

// A peer the service cannot see (pid 0) or whose pid disagrees with its pidfd is not answered.
func TestServiceRefusesAPeerItCannotSee(t *testing.T) {
	self := open(t, os.Getpid())
	for name, fix := range map[string]func(cred *unix.Ucred){
		"invisible peer (pid 0)": func(c *unix.Ucred) { c.Pid = 0 },
		"pid and pidfd disagree": func(c *unix.Ucred) { c.Pid++ },
	} {
		path, _ := startServer(t, register(t, self), func(s *Server) {
			s.peerOf = func(c *net.UnixConn) (unix.Ucred, int, error) {
				cred, fd, err := peer(c)
				fix(&cred)
				return cred, fd, err
			}
		})
		if v, err := verifyOnce(t, path, goodClient()); !errors.Is(err, ErrNotAnswered) || v != VerdictDeny {
			t.Errorf("%s: %v %v", name, v, err)
		}
	}
}

func TestServiceTimesOutASilentClient(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), func(s *Server) { s.Timeout = 300 * time.Millisecond })
	c, _ := rawHello(t, path)
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	var rep wireReply
	if err := readFrame(c, &rep); err == nil {
		t.Fatal("answered a client that sent nothing")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("a silent client held the connection for %v", time.Since(start))
	}
}

// The request must follow the hello promptly even when the connection itself may live longer.
func TestServiceDropsAVerifierThatDoesNotSendItsRequest(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), func(s *Server) { s.Timeout = 3 * time.Second; s.RequestTimeout = 200 * time.Millisecond })
	c, _ := rawHello(t, path)
	expectClosedWithoutAnswer(t, c, "a verifier that sent nothing after the hello")
}

func TestServiceLimitsTheConnectionRate(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), func(s *Server) { s.Rate = 3 })
	served, refused := 0, 0
	for i := 0; i < 12; i++ {
		c, err := DialUnix(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var h wireHello
		if err := readFrame(c, &h); err == nil {
			served++
		} else {
			refused++
		}
		_ = c.Close()
	}
	if refused == 0 || served == 0 {
		t.Errorf("rate limit: %d served, %d refused out of 12 rapid connections", served, refused)
	}
}

// A peer lookup that fails after it produced a pidfd must not keep the descriptor.
func TestServiceClosesAPidfdOfARefusedPeer(t *testing.T) {
	skipWithoutPeerPidfd(t)
	var leakedFD atomic.Int64
	leakedFD.Store(-1)
	path, _ := startServer(t, register(t, open(t, os.Getpid())), func(s *Server) {
		s.peerOf = func(c *net.UnixConn) (unix.Ucred, int, error) {
			fd, err := unix.PidfdOpen(os.Getpid(), 0)
			if err != nil {
				t.Error(err)
			}
			leakedFD.Store(int64(fd))
			return unix.Ucred{}, fd, errors.New("stub: refused")
		}
	})
	c, err := DialUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	expectClosedWithoutAnswer(t, c, "a refused peer")
	leaked := int(leakedFD.Load())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/proc/self/fd/" + strconv.Itoa(leaked)); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the pidfd (fd %d) of a refused peer is still open", leaked)
}

// flakyAcceptor fails its first accepts with a transient error, then serves one real listener.
type flakyAcceptor struct {
	l     *net.UnixListener
	mu    sync.Mutex
	fails int
}

func (a *flakyAcceptor) AcceptUnix() (*net.UnixConn, error) {
	a.mu.Lock()
	if a.fails > 0 {
		a.fails--
		a.mu.Unlock()
		return nil, syscall.EMFILE
	}
	a.mu.Unlock()
	return a.l.AcceptUnix()
}

func TestServeSurvivesTransientAcceptErrors(t *testing.T) {
	skipWithoutPeerPidfd(t)
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "s")); _ = os.Remove(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Profile: tProfile, Generation: tGen, Key: tKey, Registry: register(t, open(t, os.Getpid())), Procs: lp(t), UID: os.Getuid()}
	done := make(chan error, 1)
	go func() { done <- s.Serve(&flakyAcceptor{l: l, fails: 3}) }()
	if v, err := verifyOnce(t, path, goodClient()); err != nil || v != VerdictAllow {
		t.Fatalf("after transient accept errors: %v %v", v, err)
	}
	select {
	case err := <-done:
		t.Fatalf("Serve ended on a transient error: %v", err)
	default:
	}
	_ = l.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("Serve did not return when the listener closed")
	}
}

// flakyRegistry panics on its first lookup and then answers like a normal registry.
type flakyRegistry struct {
	mu    sync.Mutex
	calls int
	next  Registry
}

func (r *flakyRegistry) Lookup(a, b, c string) (Handle, bool) {
	r.mu.Lock()
	r.calls++
	first := r.calls == 1
	r.mu.Unlock()
	if first {
		panic("registry bug")
	}
	return r.next.Lookup(a, b, c)
}

func TestServiceSurvivesAPanicInTheRegistry(t *testing.T) {
	path, _ := startServer(t, &flakyRegistry{next: register(t, open(t, os.Getpid()))}, nil)
	if v, err := verifyOnce(t, path, goodClient()); err == nil {
		t.Fatalf("a panicking registry produced an answer: %v", v)
	}
	// the service still serves the next connection
	if v, err := verifyOnce(t, path, goodClient()); err != nil || v != VerdictAllow {
		t.Errorf("after a panic: %v %v", v, err)
	}
}

func TestServiceBoundsConcurrentConnections(t *testing.T) {
	self := open(t, os.Getpid())
	path, _ := startServer(t, register(t, self), func(s *Server) { s.MaxConns = 1; s.Timeout = 2 * time.Second })
	first, _ := rawHello(t, path) // holds the only slot: it was greeted and now says nothing
	defer first.Close()
	second, err := DialUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	var h wireHello
	err = readFrame(second, &h)
	if err == nil {
		t.Error("a second connection was served while the only slot was busy")
	} else if !errors.Is(err, io.EOF) {
		t.Errorf("the excess connection was not closed at once: %v", err)
	}
}
