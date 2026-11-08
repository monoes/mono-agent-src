//go:build windows

package procbind

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/procchain"
)

// These tests run the real service on a real Windows host: a real unix socket, the real peer-pid query, real process
// handles. Nothing replaces the kernel's side (the seams the Linux tests use are not used here).

const helperEnv = "PROCBIND_WIN_HELPER"

// shortRoot is a runtime root that is made by ListenSecure: a Windows socket path holds 107 bytes, so the name is short.
func shortRoot(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "r")
}

type fixedRegistry struct{ worker Handle }

func (r fixedRegistry) Lookup(string, string, string) (Handle, bool) {
	return r.worker, r.worker != nil
}

type svc struct {
	root, profile, gen string
	key                []byte
	l                  *SecureListener
	srv                *Server
}

func startService(t *testing.T, worker Handle, mod func(*Server)) *svc {
	t.Helper()
	root := shortRoot(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	sid, err := OwnSID()
	if err != nil {
		t.Fatal(err)
	}
	s := &svc{root: root, profile: "p1", gen: "g1", key: key}
	s.l, err = ListenSecure(SocketOptions{Root: root, Profile: s.profile, CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	procs, _ := NewProcs()
	s.srv = &Server{Profile: s.profile, Generation: s.gen, Key: key, Registry: fixedRegistry{worker}, Procs: procs, SID: sid,
		Logf: func(f string, a ...any) { t.Logf("service: "+f, a...) }}
	if mod != nil {
		mod(s.srv)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.srv.Serve(s.l) }()
	t.Cleanup(func() { _ = s.l.Close(); <-done })
	return s
}

func (s *svc) verify() (Verdict, error) {
	c, err := DialSecure(SocketOptions{Root: s.root, Profile: s.profile})
	if err != nil {
		return VerdictDeny, err
	}
	defer c.Close()
	return Client{Key: s.key, Profile: s.profile, Generation: s.gen, Timeout: 5 * time.Second}.Verify(c, "tok1", "session:1")
}

func self(t *testing.T) *ProcHandle {
	t.Helper()
	h, err := OpenProcessHandle(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// spawn starts this test binary as a helper in the given mode and returns it with its stdout reader.
func spawn(t *testing.T, mode string, env ...string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), append([]string{helperEnv + "=" + mode}, env...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, bufio.NewReader(out)
}

func readLine(t *testing.T, r *bufio.Reader, prefix string) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				ch <- ""
				return
			}
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), prefix); ok {
				ch <- v
				return
			}
		}
	}()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("no %q line from the helper", prefix)
		return ""
	}
}

// TestHelperProcess is the helper child: "sleep" waits to be killed; "verify" asks the service for a verdict.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		t.Skip("helper")
	case "sleep":
		os.Stdout.WriteString("READY\n")
		time.Sleep(5 * time.Minute)
	case "holder":
		// connects, hands the connection to a child that stays, and ends
		c, err := DialPath(filepath.Join(os.Getenv("PB_ROOT"), runDirName, "p1", sockName))
		if err != nil {
			return
		}
		rc, _ := c.SyscallConn()
		var h syscall.Handle
		_ = rc.Control(func(fd uintptr) {
			h = syscall.Handle(fd)
			_ = syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT)
		})
		b := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		b.Env = append(os.Environ(), helperEnv+"=sleep")
		b.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{h}}
		if err := b.Start(); err != nil {
			return
		}
		os.Stdout.WriteString("HOLDER " + strconv.Itoa(b.Process.Pid) + " " + strconv.Itoa(os.Getpid()) + "\n")
	case "connect":
		// connects and ends at once, leaving the connection behind
		if c, err := DialPath(filepath.Join(os.Getenv("PB_ROOT"), runDirName, "p1", sockName)); err == nil {
			_ = c
			os.Stdout.WriteString("CONNECTED\n")
		}
	case "verify":
		key, _ := hex.DecodeString(os.Getenv("PB_KEY"))
		c, err := DialSecure(SocketOptions{Root: os.Getenv("PB_ROOT"), Profile: "p1"})
		if err != nil {
			os.Stdout.WriteString("VERDICT error:dial " + err.Error() + "\n")
			return
		}
		defer c.Close()
		v, err := Client{Key: key, Profile: "p1", Generation: "g1", Timeout: 5 * time.Second}.Verify(c, "tok1", "session:1")
		if err != nil {
			os.Stdout.WriteString("VERDICT error:" + err.Error() + "\n")
			return
		}
		os.Stdout.WriteString("VERDICT " + string(v) + "\n")
	}
}

func askFromChild(t *testing.T, s *svc) string {
	t.Helper()
	_, r := spawn(t, "verify", "PB_ROOT="+s.root, "PB_KEY="+hex.EncodeToString(s.key))
	return readLine(t, r, "VERDICT ")
}

func TestReadyOnThisMachine(t *testing.T) {
	if err := Ready(); err != nil {
		t.Fatalf("the service cannot run on this Windows: %v", err)
	}
}

// The kernel's peer pid, the pinned process and its user are this process's own, through the real functions.
func TestPeerProcessNamesTheConnectorThroughTheRealKernelQuery(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan *net.UnixConn, 1)
	go func() { c, _ := l.AcceptUnix(); got <- c }()
	cl, err := DialPath(l.path)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	c := <-got
	defer c.Close()
	pi, err := peerProcess(c)
	if err != nil {
		t.Fatal(err)
	}
	defer pi.proc.Close()
	if pid, _ := pi.proc.Pid(); pid != os.Getpid() {
		t.Fatalf("the peer is pid %d, want %d", pid, os.Getpid())
	}
	sid, _ := OwnSID()
	if pi.sid != sid || sid == "" {
		t.Fatalf("the peer's user %q, want %q", pi.sid, sid)
	}
	if pi.proc.Created() != self(t).Created() {
		t.Error("the pinned creation time is not this process's")
	}
}

func TestServiceAllowsTheWorkerItself(t *testing.T) {
	s := startService(t, self(t), nil)
	if v, err := s.verify(); err != nil || v != VerdictAllow {
		t.Fatalf("the worker was not allowed: %v %v", v, err)
	}
}

func TestServiceAllowsADescendantOfTheWorker(t *testing.T) {
	s := startService(t, self(t), nil)
	if got := askFromChild(t, s); got != "allow" {
		t.Fatalf("a child of the worker: %q", got)
	}
}

func TestServiceDeniesAProcessThatIsNotInTheWorkersTree(t *testing.T) {
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	other, err := OpenProcessHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := startService(t, other, nil)
	if v, err := s.verify(); err != nil || v != VerdictDeny {
		t.Fatalf("a process outside the worker's tree: %v %v", v, err)
	}
	if got := askFromChild(t, s); got != "deny" {
		t.Fatalf("a sibling of the worker's tree: %q", got)
	}
}

func TestServiceDeniesWhenTheWorkerHasExited(t *testing.T) {
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	w, err := OpenWorkerHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s := startService(t, w, nil)
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if w.Alive() {
		t.Fatal("the worker is still alive")
	}
	if v, err := s.verify(); err != nil || v != VerdictDeny {
		t.Fatalf("an exited worker: %v %v", v, err)
	}
}

func TestServiceAnswersNobodyOfAnotherUser(t *testing.T) {
	for name, sid := range map[string]string{"another user": "S-1-5-21-1-2-3-1001", "no user": ""} {
		s := startService(t, self(t), func(sv *Server) { sv.SID = sid })
		v, err := s.verify()
		if !errors.Is(err, ErrNotAnswered) || v != VerdictDeny {
			t.Errorf("%s: %v %v, want no answer", name, v, err)
		}
	}
}

func TestOpenProcessHandleRefusesAnExitedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// the exited child's object is gone with cmd.Run; a number that names nothing is refused too
	if h, err := OpenProcessHandle(cmd.Process.Pid); err == nil {
		h.Close()
		t.Fatal("an exited process was pinned")
	}
	// one whose object is still held: it exits, and is refused although it can be opened
	c2 := exec.Command(os.Args[0], "-test.run=^$")
	if err := c2.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c2.Process.Pid
	defer c2.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		h, err := OpenProcessHandle(pid)
		if err != nil {
			return // refused: exited
		}
		h.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a process that has exited was still pinned")
}

func TestWalkOverRealProcessesChecksCreationOrder(t *testing.T) {
	// a real child names this process as its parent, and this process was created first
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	child, err := OpenProcessHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	procs := WinProcs{}
	if p, err := procs.Parent(child); err != nil || p != os.Getpid() {
		t.Fatalf("parent %d %v", p, err)
	}
	if child.Created() < self(t).Created() {
		t.Fatalf("the child (%d) is older than its parent (%d)", child.Created(), self(t).Created())
	}
	if r := Walk(procs, child, self(t)); !r.Allowed {
		t.Fatalf("a real child of the worker: %s", r)
	}
	if r := Walk(procs, self(t), child); r.Allowed {
		t.Fatal("a parent counted as a descendant of its child")
	}
}

// The creation time the walk compares is the process's own, the same one the ancestry reader stamps with.
func TestWinProcsStartIsTheCreationTimeOfTheProcess(t *testing.T) {
	h := self(t)
	got, err := WinProcs{}.Start(h)
	if err != nil || got == 0 {
		t.Fatalf("start %d %v", got, err)
	}
	if want := "start:" + strconv.FormatUint(got, 10); procchain.Stamp(os.Getpid()) != want {
		t.Fatalf("the walk's start %q is not the stamp %q", want, procchain.Stamp(os.Getpid()))
	}
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	c, err := OpenProcessHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if s, _ := (WinProcs{}).Start(c); s <= got {
		t.Fatalf("a child (%d) is not younger than this process (%d)", s, got)
	}
}

// A connector that has already exited when the service looks at it is not served, even while its process object still
// exists (a handle to it is held elsewhere): an exited process is never taken for the live one that held its number.
func TestPeerThatExitedBeforeItWasPinnedIsRefused(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd, r := spawn(t, "connect", "PB_ROOT="+root)
	readLine(t, r, "CONNECTED")
	w, err := OpenProcessHandle(cmd.Process.Pid) // keeps the exited process's object, and so its number, alive
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for w.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if w.Alive() {
		t.Fatal("the connector did not exit")
	}
	// Windows drops a connection whose client has gone before it is accepted; if one is handed over it must be refused
	_ = l.l.SetDeadline(time.Now().Add(3 * time.Second))
	c, err := l.AcceptUnix()
	if err != nil {
		t.Logf("the dead connector's connection was not handed over: %v", err)
		return
	}
	defer c.Close()
	if pi, err := peerProcess(c); err == nil {
		pi.proc.Close()
		t.Fatal("a connector that had exited was pinned and would be served")
	}
}

// The kernel reports the connector as the peer of a connection that it handed to another process and then ended (the
// holder is not named), and an ended connector is not served.
func TestPeerPIDStaysTheConnectorsAfterItHandedTheConnectionOn(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd, r := spawn(t, "holder", "PB_ROOT="+root)
	f := strings.Fields(readLine(t, r, "HOLDER "))
	if len(f) != 2 {
		t.Fatalf("holder line %v", f)
	}
	_ = cmd.Wait()
	_ = l.l.SetDeadline(time.Now().Add(5 * time.Second))
	c, err := l.AcceptUnix()
	if err != nil {
		t.Fatalf("the connection handed to a child was dropped with its connector: %v", err)
	}
	defer c.Close()
	pid, err := peerPID(c)
	if err != nil || strconv.Itoa(int(pid)) != f[1] {
		t.Fatalf("the kernel names pid %d (err %v), not the connector %s (the holder is %s)", pid, err, f[1], f[0])
	}
	// the number is the connector's, which has ended: it is refused. The residual (documented): once that number is
	// recycled to a live process, that process is the one pinned, because the kernel does not say who holds the connection.
	if pi, perr := peerProcess(c); perr == nil {
		pi.proc.Close()
		t.Fatal("the service would serve a connection whose connector has ended")
	}
}

// KNOWN GAP, pinned so that nobody mistakes it for covered: a Windows process records the parent its creator names, so a
// process of the same user that is not in the worker's tree can create a child that claims a member of the tree as its
// parent, and the child then passes the parent walk (and inherits that parent's job, so a job would not tell it apart).
// Linux has no equivalent: its parent is the kernel's. If this ever starts to deny, the gap is closed: update SECURITY.md.
func TestKnownGapAProcessThatClaimsTheWorkerAsItsParentIsServed(t *testing.T) {
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	other, err := OpenProcessHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := startService(t, other, nil)
	if v, _ := s.verify(); v != VerdictDeny {
		t.Fatalf("control: this process is not in the worker's tree: %v", v)
	}
	ph, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("the worker cannot be named as a parent any more (%v): the gap may be closed; update the documentation and this test", err)
	}
	defer windows.CloseHandle(ph)
	c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	c.Env = append(os.Environ(), helperEnv+"=verify", "PB_ROOT="+s.root, "PB_KEY="+hex.EncodeToString(s.key))
	c.SysProcAttr = &syscall.SysProcAttr{ParentProcess: syscall.Handle(ph)}
	out, err := c.Output()
	if err != nil {
		t.Fatalf("a process with a named parent cannot be made (%v): the gap may be closed; update the documentation and this test", err)
	}
	if !strings.Contains(string(out), "VERDICT allow") {
		t.Fatalf("the known gap is closed (%q): update the documentation and this test", out)
	}
}

// KNOWN GAP, second route: under the default access list the same user may open any process of its own with the rights to
// write into it and start a thread in it (CreateRemoteThread), and the code that then runs is the member: its peer pid, user
// and ancestry are genuine. No check of the service sees it. Hardening the access list of the turn is separate work; when it
// lands this test must fail and be turned into a must-deny test.
func TestKnownGapTheDefaultAccessListLetsTheSameUserWriteIntoATreeMember(t *testing.T) {
	cmd, r := spawn(t, "sleep")
	readLine(t, r, "READY")
	const want = windows.PROCESS_VM_WRITE | windows.PROCESS_VM_OPERATION | windows.PROCESS_CREATE_THREAD | windows.PROCESS_VM_READ
	h, err := windows.OpenProcess(want, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("the member can no longer be opened for writing (%v): the gap may be closed; update the documentation and this test", err)
	}
	windows.CloseHandle(h)
}

func TestReadyNamesALongTemporaryFolderAsTheCause(t *testing.T) {
	old := readyProbes.peerPID
	readyProbes.peerPID = func() error { return errProbePath }
	defer func() { readyProbes.peerPID = old }()
	if err := Ready(); err == nil || !strings.Contains(err.Error(), "TEMP") || strings.Contains(err.Error(), "1809") {
		t.Fatalf("Ready: %v", err)
	}
	readyProbes.peerPID = func() error { return errors.New("x") }
	if err := Ready(); err == nil || !strings.Contains(err.Error(), "1809") {
		t.Fatalf("Ready: %v", err)
	}
}
