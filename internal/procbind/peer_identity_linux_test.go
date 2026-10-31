//go:build linux

package procbind

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// pk is the key of a peer that started at tick 1: the tests that do not care about the start time.
func pk(pid int) peerKey { return peerKey{pid: pid, start: 1} }

// A pid the kernel hands to a new process is a different peer. The predecessor's drained allowance is
// not inherited, but the same process (same pid, same start time) stays held to it.
func TestARecycledPidStartsWithAFullAllowance(t *testing.T) {
	tr := newTestTracker(8, 4, 2) // two connections per second per peer, the clock does not move
	old := peerKey{pid: 4242, start: 100}
	for i := 0; i < 2; i++ {
		c, _ := unixPair(t)
		cs, why := tr.admit(old, c, yes)
		if cs == nil {
			t.Fatalf("connection %d of the old process: %s", i, why)
		}
		tr.release(cs)
	}
	c, _ := unixPair(t)
	if cs, _ := tr.admit(old, c, yes); cs != nil {
		t.Fatal("the same process was admitted past its rate")
	}
	reborn := peerKey{pid: 4242, start: 200} // the number was recycled inside the refill window
	c2, _ := unixPair(t)
	if cs, why := tr.admit(reborn, c2, yes); cs == nil {
		t.Errorf("a new process with a recycled pid inherited its predecessor's drained allowance: %s", why)
	}
}

// The connections of a recycled pid are not counted against its predecessor's cap either, and the
// shared bounds still hold: the total, and the table of records.
func TestIdentitiesOfOnePidAreCountedSeparatelyButStillBounded(t *testing.T) {
	tr := newTestTracker(4, 1, 1000)
	var held []*connState
	for start := uint64(1); start <= 4; start++ {
		c, _ := unixPair(t)
		cs, why := tr.admit(peerKey{pid: 7, start: start}, c, yes)
		if cs == nil {
			t.Fatalf("start %d: %s", start, why)
		}
		held = append(held, cs)
	}
	if tr.total != 4 {
		t.Fatalf("total = %d, want 4", tr.total)
	}
	c, _ := unixPair(t)
	if cs, _ := tr.admit(peerKey{pid: 7, start: 5}, c, no); cs != nil {
		t.Error("the shared total was passed")
	}
	for _, cs := range held {
		tr.release(cs)
	}
	// many short-lived identities of one pid: the table of records stays within its bound
	for start := uint64(10); start < 10+uint64(maxPeerRecords(tr.tracker))+500; start++ {
		c, _ := unixPair(t)
		cs, why := tr.admit(peerKey{pid: 7, start: start}, c, yes)
		if cs == nil {
			t.Fatalf("start %d: %s", start, why)
		}
		tr.release(cs)
		if n := len(tr.peers); n > maxPeerRecords(tr.tracker) {
			t.Fatalf("the peer table holds %d records, bound %d", n, maxPeerRecords(tr.tracker))
		}
	}
}

// The identity is read from the kernel's view of the peer: this test process, whose start time is its own.
func TestPeerIdentityIsThePidAndStartTimeOfTheOtherEnd(t *testing.T) {
	if !PeerPidfdSupported() {
		t.Skip("no SO_PEERPIDFD on this kernel")
	}
	a, _ := unixPair(t)
	key, err := peerIdentity(a, peer)
	if err != nil {
		t.Fatal(err)
	}
	want, err := readStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if key.pid != os.Getpid() || key.start != want || key.start == 0 {
		t.Errorf("identity = %+v, want pid %d start %d", key, os.Getpid(), want)
	}
}

// No identity is taken from a pidfd that does not name the pid the kernel reported, from a pid this
// service cannot see, or from a process that was reaped before its start time could be trusted.
func TestPeerIdentityRefusesWhatItCannotPin(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pfd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		t.Skipf("pidfd_open: %v", err)
	}
	dupfd := func() int {
		fd, err := unix.FcntlInt(uintptr(pfd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		return fd
	}
	c, _ := unixPair(t)
	look := func(pid int32, fd func() int) func(*net.UnixConn) (unix.Ucred, int, error) {
		return func(*net.UnixConn) (unix.Ucred, int, error) {
			return unix.Ucred{Pid: pid, Uid: uint32(os.Getuid())}, fd(), nil
		}
	}
	// the pidfd names the child, the credentials say another pid
	if _, err := peerIdentity(c, look(int32(os.Getpid()), dupfd)); err == nil || !strings.Contains(err.Error(), "does not name") {
		t.Errorf("a pidfd for another process gave an identity: %v", err)
	}
	// a pid the service cannot see
	if _, err := peerIdentity(c, look(0, dupfd)); err == nil || !strings.Contains(err.Error(), "not visible") {
		t.Errorf("pid 0 gave an identity: %v", err)
	}
	// the right pid, while the process lives: an identity
	if key, err := peerIdentity(c, look(int32(cmd.Process.Pid), dupfd)); err != nil || key.pid != cmd.Process.Pid || key.start == 0 {
		t.Errorf("the live child: %+v %v", key, err)
	}
	// reaped before the read: its number may belong to someone else now
	fd := dupfd()
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h := HandleFromPidfd(dupfd()); func() bool { defer h.Close(); _, err := h.Pid(); return err != nil }() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if key, err := peerIdentity(c, look(int32(cmd.Process.Pid), func() int { return fd })); err == nil {
		t.Errorf("a reaped process gave an identity: %+v", key)
	}
}

// A process reaped between the pidfd check and the end of the procfs read may have its number given to
// another process, whose start time the read could have returned: no identity then.
func TestPeerIdentityRefusesAProcessReapedWhileItsStartTimeWasRead(t *testing.T) {
	if !PeerPidfdSupported() {
		t.Skip("no SO_PEERPIDFD on this kernel")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pfd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		t.Skipf("pidfd_open: %v", err)
	}
	probe, err := unix.FcntlInt(uintptr(pfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(probe)
	orig := startTimeOf
	defer func() { startTimeOf = orig }()
	startTimeOf = func(pid int) (uint64, error) {
		v, err := orig(pid)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait() // reaped: the number is free to be reused
		h := HandleFromPidfd(probe)
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, perr := h.Pid(); perr != nil {
				break
			}
		}
		return v, err
	}
	c, _ := unixPair(t)
	look := func(*net.UnixConn) (unix.Ucred, int, error) {
		return unix.Ucred{Pid: int32(cmd.Process.Pid), Uid: uint32(os.Getuid())}, pfd, nil
	}
	if key, err := peerIdentity(c, look); err == nil || !strings.Contains(err.Error(), "reaped") {
		t.Errorf("a process reaped during the read gave an identity: %+v %v", key, err)
	}
}

// The accept loop keys limits by the identity it reads: a second process with the same pid is not held to
// the first one's rate.
func TestServeKeysThePerPeerRateByIdentity(t *testing.T) {
	var starts = make(chan uint64, 8)
	path, _ := fairServer(t, func(s *Server) {
		s.MaxConns = 8
		s.MaxPerPeer = 8
		s.Rate = 1000
		s.PerPeerRate = 1 // one connection per second per process, burst one
		s.identityOf = func(*net.UnixConn) (peerKey, error) { return peerKey{pid: 99, start: <-starts}, nil }
	})
	greeted := func(start uint64) bool {
		starts <- start
		c, err := DialUnix(path)
		if err != nil {
			return false
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		var h wireHello
		return readFrame(c, &h) == nil
	}
	if !greeted(1) {
		t.Fatal("the first connection was not greeted")
	}
	if greeted(1) {
		t.Error("the same process was greeted twice inside its refill window")
	}
	if !greeted(2) {
		t.Error("a new process with the same pid was refused with its predecessor's allowance")
	}
}
