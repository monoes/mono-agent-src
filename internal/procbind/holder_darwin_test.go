//go:build darwin

package procbind

import (
	"bufio"
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

// TestDarwinHelperPeerPidConnector connects, reports its pid, waits for the test's go, then runs a child that inherited the
// connection and writes to it.
func TestDarwinHelperPeerPidConnector(t *testing.T) {
	sock := os.Getenv("PROCBIND_DARWIN_PEERPID_SOCK")
	if sock == "" {
		t.Skip("helper process only")
	}
	if os.Getenv("PROCBIND_DARWIN_PEERPID_WRITER") != "" {
		c, err := net.FileConn(os.NewFile(3, "conn"))
		if err == nil {
			fmt.Println("WRITER", os.Getpid())
			_, _ = c.Write([]byte("x"))
		}
		return
	}
	conn, err := DialUnix(sock)
	if err != nil {
		return
	}
	f, _ := conn.File()
	fmt.Println("CONNECTOR", os.Getpid())
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	child := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperPeerPidConnector$")
	child.Env = append(os.Environ(), "PROCBIND_DARWIN_PEERPID_WRITER=1")
	child.ExtraFiles = []*os.File{f}
	child.Stdout = os.Stdout
	_ = child.Run()
	time.Sleep(2 * time.Second) // keeps its end while the test reads
}

// Measured on macOS 27: LOCAL_PEERPID is the process that last did I/O on the other end, not only the one that connected.
// Right after the accept it is the connector; once a process holding a duplicate has written, it is that process. The
// service reads it at accept and again when it serves the connection, and the holder proof is made for the pid it reads then.
func TestDarwinPeerPidFollowsTheProcessThatLastDidIO(t *testing.T) {
	path := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperPeerPidConnector$", "-test.v")
	cmd.Env = append(os.Environ(), "PROCBIND_DARWIN_PEERPID_SOCK="+path)
	in, _ := cmd.StdinPipe()
	pipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	r := bufio.NewReader(pipe)
	pidOf := func(tag string) int {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("no %s line: %v", tag, err)
			}
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(line), tag+" %d", &n); err == nil {
				return n
			}
		}
	}
	connector := pidOf("CONNECTOR")
	c, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cred, _, err := peer(c)
	if err != nil || int(cred.Pid) != connector {
		t.Fatalf("at accept: %+v %v, the connector is %d", cred, err, connector)
	}
	_, _ = in.Write([]byte("go\n"))
	writer := pidOf("WRITER")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatalf("the writer's byte did not arrive: %v", err)
	}
	cred, _, err = peer(c)
	if err != nil || connector == writer || int(cred.Pid) != writer {
		t.Fatalf("after the writer %d wrote: %+v %v (the connector is %d)", writer, cred, err, connector)
	}
}

// --- the negative control of the layout self-check ---

// fakeTable gives each descriptor its (own, conn) pair.
func fakeTable(m map[int][2]uint64) func(int) (uint64, uint64, error) {
	return func(fd int) (uint64, uint64, error) {
		if p, ok := m[fd]; ok {
			return p[0], p[1], nil
		}
		return 0, 0, errors.New("unreadable")
	}
}

func TestDarwinCheckLayoutNeedsRealPairsAndANegativeControl(t *testing.T) {
	a, b := [2]int{1, 2}, [2]int{3, 4}
	good := map[int][2]uint64{1: {10, 11}, 2: {11, 10}, 3: {20, 21}, 4: {21, 20}}
	if err := checkLayout(fakeTable(good), a, b); err != nil {
		t.Fatalf("a consistent table: %v", err)
	}
	same := map[int][2]uint64{}
	for fd := 1; fd <= 4; fd++ {
		same[fd] = [2]uint64{7, 7} // both offsets read the same field, the same for every socket
	}
	if err := checkLayout(fakeTable(same), a, b); err == nil {
		t.Error("offsets that read one field twice passed")
	}
	sameField := map[int][2]uint64{1: {10, 10}, 2: {11, 11}, 3: {20, 20}, 4: {21, 21}} // own == conn for each socket
	if err := checkLayout(fakeTable(sameField), a, b); err == nil {
		t.Error("sockets that are their own peers passed")
	}
	crossed := map[int][2]uint64{1: {10, 11}, 2: {11, 10}, 3: {10, 11}, 4: {11, 10}} // b is a second view of a's connection
	if err := checkLayout(fakeTable(crossed), a, b); err == nil {
		t.Error("two pairs that are the same connection passed (no negative control)")
	}
	notPairs := map[int][2]uint64{1: {10, 11}, 2: {12, 10}, 3: {20, 21}, 4: {21, 20}}
	if err := checkLayout(fakeTable(notPairs), a, b); err == nil {
		t.Error("ends that are not connected to each other passed")
	}
	zero := map[int][2]uint64{1: {0, 0}, 2: {0, 0}, 3: {20, 21}, 4: {21, 20}}
	if err := checkLayout(fakeTable(zero), a, b); err == nil {
		t.Error("zero control blocks passed")
	}
	if err := checkLayout(fakeTable(map[int][2]uint64{}), a, b); err == nil {
		t.Error("unreadable descriptors passed")
	}
}
