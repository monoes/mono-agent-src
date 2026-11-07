//go:build darwin

package procbind

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// --- the matching loop, on a fake table ---

func TestDarwinFindHolderNeedsBothControlBlocks(t *testing.T) {
	const own, peerPCB = 10, 20
	table := map[int][2]uint64{
		3: {20, 99}, // the right socket, connected to another
		4: {99, 10}, // another socket, connected to us
		5: {20, 10}, // the connector's end
	}
	info := func(fd int) (uint64, uint64, error) {
		if p, ok := table[fd]; ok {
			return p[0], p[1], nil
		}
		return 0, 0, errors.New("unreadable")
	}
	far := time.Now().Add(time.Minute)
	if err := findHolder([]int{3, 4, 5}, info, own, peerPCB, far); err != nil {
		t.Fatalf("the holder was not found: %v", err)
	}
	if err := findHolder([]int{3}, info, own, peerPCB, far); err == nil {
		t.Error("a socket with the right control block but another peer was taken for the holder")
	}
	if err := findHolder([]int{4}, info, own, peerPCB, far); err == nil {
		t.Error("a socket connected to us but with another control block was taken for the holder")
	}
	if err := findHolder([]int{7, 8}, info, own, peerPCB, far); err == nil {
		t.Error("unreadable descriptors were taken for the holder")
	}
	if err := findHolder([]int{5}, info, own, peerPCB, time.Now().Add(-time.Second)); !errors.Is(err, errHolderBudget) {
		t.Errorf("past the deadline: %v", err)
	}
}

// --- the bound, on the real table ---

func connectedPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	path := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	cl, err := DialUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	sv, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sv.Close() })
	return cl, sv
}

func TestDarwinHolderProofIsBoundedByTheNumberOfDescriptors(t *testing.T) {
	cl, sv := connectedPair(t)
	if err := holdsConnection(sv, os.Getpid()); err != nil {
		t.Fatalf("the process that holds the client end: %v", err)
	}
	_ = cl
	var pairs [][2]int
	t.Cleanup(func() {
		for _, p := range pairs {
			unix.Close(p[0])
			unix.Close(p[1])
		}
	})
	for i := 0; i < 40; i++ {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, [2]int{fds[0], fds[1]})
	}
	old := maxHolderFDs
	maxHolderFDs = 32
	t.Cleanup(func() { maxHolderFDs = old })
	if err := holdsConnection(sv, os.Getpid()); err == nil {
		t.Fatal("a process with more descriptors than the bound was examined")
	}
	maxHolderFDs = old
	if err := holdsConnection(sv, os.Getpid()); err != nil {
		t.Errorf("under the bound: %v", err)
	}
}

func TestDarwinHolderProofIsBoundedByTime(t *testing.T) {
	_, sv := connectedPair(t)
	old := holderBudget
	holderBudget = time.Nanosecond
	t.Cleanup(func() { holderBudget = old })
	if err := holdsConnection(sv, os.Getpid()); err == nil {
		t.Error("the proof ran past its budget")
	}
}

// --- where the proof runs ---

// The proof is not in the accept loop: while one connection's proof is stuck, another is accepted, proved and answered; and
// every connection is proved once.
func TestDarwinTheProofIsNotInTheAcceptLoopAndRunsOnce(t *testing.T) {
	self, _ := NewProcHandle(os.Getpid())
	var calls atomic.Int32
	release := make(chan struct{})
	var first sync.Once
	old := holderProof
	holderProof = func(c *net.UnixConn, pid int) error {
		calls.Add(1)
		stuck := false
		first.Do(func() { stuck = true })
		if stuck {
			<-release
		}
		return old(c, pid)
	}
	t.Cleanup(func() { holderProof = old })
	sock := startDServer(t, &dReg{h: self}, nil)
	verify := func() (Verdict, error) {
		conn, err := DialUnix(sock)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return dClient().Verify(conn, dToken, dScope)
	}
	stuckDone := make(chan struct{})
	go func() { _, _ = verify(); close(stuckDone) }()
	for calls.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	got := make(chan Verdict, 1)
	go func() { v, _ := verify(); got <- v }()
	select {
	case v := <-got:
		if v != VerdictAllow {
			t.Errorf("the second connection got %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stuck proof of one connection held up the next: the proof is in the accept loop")
	}
	close(release)
	<-stuckDone
	if n := calls.Load(); n != 2 {
		t.Errorf("%d proofs for 2 connections, want one each", n)
	}
}

// --- the layout self-check ---

func TestDarwinTheLayoutSelfCheckPassesHereAndFailsReadyWhenItDoesNot(t *testing.T) {
	if err := layoutCheck(); err != nil {
		t.Fatalf("layoutCheck on this machine: %v", err)
	}
	if err := Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	old := readyProbes
	t.Cleanup(func() { readyProbes = old })
	readyProbes.layout = func() error { return errors.New("moved") }
	err := Ready()
	if err == nil || !strings.Contains(err.Error(), "socket information") {
		t.Errorf("Ready with a moved layout: %v", err)
	}
}

// --- what LOCAL_PEERPID names ---

// TestDarwinHelperPeerPidConnector connects and starts a child that inherits the connection and writes to it; it then closes
// its own copy, so the last process to do I/O on the connection is the child.
func TestDarwinHelperPeerPidConnector(t *testing.T) {
	sock := os.Getenv("PROCBIND_DARWIN_PEERPID_SOCK")
	if sock == "" {
		t.Skip("helper process only")
	}
	if os.Getenv("PROCBIND_DARWIN_PEERPID_WRITER") != "" {
		c, err := net.FileConn(os.NewFile(3, "conn"))
		if err == nil {
			_, _ = c.Write([]byte("x"))
		}
		return
	}
	conn, err := DialUnix(sock)
	if err != nil {
		return
	}
	f, _ := conn.File()
	child := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperPeerPidConnector$")
	child.Env = append(os.Environ(), "PROCBIND_DARWIN_PEERPID_WRITER=1")
	child.ExtraFiles = []*os.File{f}
	_ = child.Run()
	fmt.Println("CONNECTOR", os.Getpid(), "WRITER", child.Process.Pid)
	time.Sleep(2 * time.Second) // keeps its end while the test reads
}

// LOCAL_PEERPID is the process that connected, not the process that last did I/O on the connection (measured on macOS 27:
// the pid read after another process, holding a duplicate, wrote to the socket is still the connector's).
func TestDarwinPeerPidIsTheConnectorNotTheLastIOProcess(t *testing.T) {
	path := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperPeerPidConnector$", "-test.v")
	cmd.Env = append(os.Environ(), "PROCBIND_DARWIN_PEERPID_SOCK="+path)
	outc := make(chan string, 1)
	pipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	go func() {
		b := make([]byte, 4096)
		var acc string
		for {
			n, err := pipe.Read(b)
			acc += string(b[:n])
			if strings.Contains(acc, "CONNECTOR") && strings.Contains(acc, "WRITER") && strings.Contains(acc, "\n") {
				outc <- acc
				return
			}
			if err != nil {
				outc <- acc
				return
			}
		}
	}()
	c, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var line string
	select {
	case line = <-outc:
	case <-time.After(15 * time.Second):
		t.Fatal("no report from the helper")
	}
	var connector, writer int
	for _, l := range strings.Split(line, "\n") {
		if strings.HasPrefix(l, "CONNECTOR") {
			fmt.Sscanf(l, "CONNECTOR %d WRITER %d", &connector, &writer)
		}
	}
	buf := make([]byte, 1)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("the writer's byte did not arrive: %v", err)
	}
	cred, _, err := peer(c)
	if err != nil {
		t.Fatal(err)
	}
	if connector == 0 || writer == 0 || connector == writer {
		t.Fatalf("helper report %q", line)
	}
	if int(cred.Pid) != connector {
		t.Fatalf("LOCAL_PEERPID after the writer %d wrote = %d, the connector is %d", writer, cred.Pid, connector)
	}
}
