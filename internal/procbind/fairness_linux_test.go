//go:build linux

package procbind

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A same-uid process must not be able to take every connection slot or the whole connection rate and
// so deny the verifications of others (the service then fails closed for them). The flooder runs as a
// separate process, because the service tells peers apart by the process that connected.

// TestHelperFlood is the flooding process: PROCBIND_FLOOD=hold opens N connections and keeps them
// open without sending a request; =burst opens and closes N connections as fast as it can. It prints
// SERVED=<how many were greeted> and, for hold, waits for its stdin to close.
func TestHelperFlood(t *testing.T) {
	mode := os.Getenv("PROCBIND_FLOOD")
	if mode == "" {
		t.Skip("helper process only")
	}
	n, _ := strconv.Atoi(os.Getenv("PROCBIND_FLOOD_N"))
	sock := os.Getenv("PROCBIND_SOCK")
	served := 0
	var held []net.Conn
	for i := 0; i < n; i++ {
		c, err := DialUnix(sock)
		if err != nil {
			continue
		}
		_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		var h wireHello
		if readFrame(c, &h) == nil {
			served++
		}
		if mode == "hold" {
			_ = c.SetReadDeadline(time.Time{})
			held = append(held, c)
		} else {
			_ = c.Close()
		}
	}
	fmt.Printf("SERVED=%d\n", served)
	if mode == "hold" {
		_, _ = io.Copy(io.Discard, os.Stdin) // keep the connections until the parent is done
	}
	for _, c := range held {
		_ = c.Close()
	}
}

type flooder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	served int
}

func startFlooder(t *testing.T, sock, mode string, n int) *flooder {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperFlood$")
	cmd.Env = append(os.Environ(), "PROCBIND_FLOOD="+mode, "PROCBIND_FLOOD_N="+strconv.Itoa(n), "PROCBIND_SOCK="+sock)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &flooder{cmd: cmd, stdin: in, served: -1}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "SERVED="); ok {
			f.served, _ = strconv.Atoi(v)
			break
		}
	}
	if f.served < 0 {
		t.Fatal("the flooder reported nothing")
	}
	return f
}

func fairServer(t *testing.T, tweak func(*Server)) (string, *Server) {
	self := open(t, os.Getpid())
	return startServer(t, register(t, self), func(s *Server) {
		s.Timeout = 10 * time.Second
		s.RequestTimeout = 5 * time.Second // the flooder holds idle connections for the whole test
		tweak(s)
	})
}

// One process holds at most MaxPerPeer connections, however many it opens, and others are still served.
func TestAPeerCannotHoldMoreThanItsShareOfConnections(t *testing.T) {
	path, _ := fairServer(t, func(s *Server) { s.MaxConns = 8; s.MaxPerPeer = 2; s.Rate = 1000; s.PerPeerRate = 1000 })
	f := startFlooder(t, path, "hold", 30)
	if f.served > 2 {
		t.Errorf("one process was greeted on %d connections, want at most MaxPerPeer=2", f.served)
	}
	if v, err := verifyOnce(t, path, goodClient()); err != nil || v != VerdictAllow {
		t.Errorf("another process while the flooder holds its share: %v %v", v, err)
	}
}

// When every slot is taken, a newcomer with fewer connections takes an idle one from the heaviest peer.
func TestANewcomerTakesAnIdleSlotFromTheHeaviestPeer(t *testing.T) {
	path, _ := fairServer(t, func(s *Server) { s.MaxConns = 2; s.MaxPerPeer = 2; s.Rate = 1000; s.PerPeerRate = 1000 })
	f := startFlooder(t, path, "hold", 10)
	if f.served != 2 {
		t.Fatalf("the flooder should fill both slots, was greeted %d times", f.served)
	}
	if v, err := verifyOnce(t, path, goodClient()); err != nil || v != VerdictAllow {
		t.Errorf("the service was full of one process's idle connections and refused another: %v %v", v, err)
	}
}

// One process cannot spend the whole connection rate.
func TestAPeerCannotSpendTheWholeConnectionRate(t *testing.T) {
	path, _ := fairServer(t, func(s *Server) { s.MaxConns = 32; s.MaxPerPeer = 32; s.Rate = 2; s.PerPeerRate = 1 })
	f := startFlooder(t, path, "burst", 200)
	if f.served > 3 {
		t.Errorf("one process was greeted on %d of 200 rapid connections with a per-process rate of 1", f.served)
	}
	if v, err := verifyOnce(t, path, goodClient()); err != nil || v != VerdictAllow {
		t.Errorf("the flood used the shared rate and another process was refused: %v %v", v, err)
	}
}

func unixPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "pair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	a, b := mk(fds[0]), mk(fds[1])
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

// A connection whose request has been read is working: a newcomer never takes its slot, only an idle one.
func TestTrackerNeverEvictsABusyConnection(t *testing.T) {
	tr := tracker{max: 2, perPeer: 2, perRate: 1000, peers: map[int]*peerState{}}
	c1, c1peer := unixPair(t)
	c2, _ := unixPair(t)
	a, why := tr.admit(100, c1)
	if a == nil {
		t.Fatal(why)
	}
	b, why := tr.admit(100, c2)
	if b == nil {
		t.Fatal(why)
	}
	tr.busy(a) // one of the heaviest peer's two connections is working, the other waits
	n1, _ := unixPair(t)
	got, why := tr.admit(200, n1)
	if got == nil {
		t.Fatalf("a newcomer was refused although the heaviest peer has an idle connection: %s", why)
	}
	// the idle one (b) was closed to make room; the busy one (a) is untouched
	if err := c1peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := c1peer.Read(make([]byte, 1)); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("the busy connection was closed: %v", err)
	}
	if !b.removed || a.removed {
		t.Errorf("removed: idle=%v busy=%v, want idle removed and busy kept", b.removed, a.removed)
	}
	// now every connection of the heaviest peer is busy: the next newcomer finds nothing to take
	tr.busy(got)
	tr2 := tracker{max: 1, perPeer: 2, perRate: 1000, peers: map[int]*peerState{}}
	d1, _ := unixPair(t)
	x, why := tr2.admit(100, d1)
	if x == nil {
		t.Fatal(why)
	}
	tr2.busy(x)
	d2, _ := unixPair(t)
	if y, _ := tr2.admit(200, d2); y != nil {
		t.Error("a newcomer took the slot of a busy connection")
	}
}

// Releasing every connection returns the slots and forgets the peer.
func TestTrackerReleaseReturnsTheSlotAndForgetsThePeer(t *testing.T) {
	tr := tracker{max: 1, perPeer: 1, perRate: 1, peers: map[int]*peerState{}}
	c, _ := unixPair(t)
	a, why := tr.admit(7, c)
	if a == nil {
		t.Fatal(why)
	}
	tr.release(a)
	tr.release(a) // idempotent
	if tr.total != 0 {
		t.Errorf("total = %d after release", tr.total)
	}
	d, _ := unixPair(t)
	if b, _ := tr.admit(8, d); b == nil {
		t.Error("the slot was not returned")
	}
}

type gateRegistry struct {
	entered chan struct{}
	release chan struct{}
	next    Registry
}

func (g *gateRegistry) Lookup(a, b, c string) (Handle, bool) {
	close(g.entered)
	<-g.release
	return g.next.Lookup(a, b, c)
}

// A verification in progress (its request read, its walk running) is never cut off by a newcomer.
func TestAVerificationInProgressIsNotEvicted(t *testing.T) {
	g := &gateRegistry{entered: make(chan struct{}), release: make(chan struct{}), next: register(t, open(t, os.Getpid()))}
	path, _ := startServer(t, g, func(s *Server) {
		s.MaxConns = 1
		s.MaxPerPeer = 1
		s.Rate = 1000
		s.PerPeerRate = 1000
		s.Timeout = 10 * time.Second
	})
	type result struct {
		v   Verdict
		err error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := DialUnix(path)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer conn.Close()
		v, err := goodClient().Verify(conn, tToken, tScope)
		done <- result{v, err}
	}()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the verification never reached the registry")
	}
	// another process arrives while the only slot is working: it must be refused, not take the slot
	f := startFlooder(t, path, "burst", 1)
	if f.served != 0 {
		t.Errorf("a newcomer was greeted while the only slot was busy (%d)", f.served)
	}
	close(g.release)
	select {
	case r := <-done:
		if r.err != nil || r.v != VerdictAllow {
			t.Errorf("the verification in progress was cut off: %v %v", r.v, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the verification did not finish")
	}
}
