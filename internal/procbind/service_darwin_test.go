//go:build darwin

package procbind

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/procchain"
)

// The binding service on macOS, against the real kernel: LOCAL_PEERCRED and LOCAL_PEERPID for the peer, the sysctl
// process table for the walk, the checked socket directory.

const (
	dGen     = "gen-darwin"
	dProfile = "p1"
	dScope   = "session:7"
	dToken   = "tok-1"
)

var dKey = bytes.Repeat([]byte{9}, 32)

type dReg struct {
	mu sync.Mutex
	h  Handle
}

func (r *dReg) Lookup(tokenID, profile, scope string) (Handle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.h, r.h != nil && tokenID == dToken && profile == dProfile && scope == dScope
}

func dClient() Client {
	return Client{Key: dKey, Profile: dProfile, Generation: dGen, Timeout: 3 * time.Second}
}

// shortDir is a private directory with a path short enough for a socket address (a macOS temp dir is not).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startDServer serves registry reg on a plain socket until the test ends.
func startDServer(t *testing.T, reg Registry, tweak func(*Server)) string {
	t.Helper()
	path := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	procs, _ := NewProcs()
	var mu sync.Mutex
	over := false
	t.Cleanup(func() { mu.Lock(); over = true; mu.Unlock() })
	s := &Server{Profile: dProfile, Generation: dGen, Key: dKey, Registry: reg, Procs: procs, UID: os.Getuid(),
		Timeout: 3 * time.Second, Logf: func(f string, a ...any) {
			mu.Lock()
			defer mu.Unlock()
			if !over {
				t.Logf("server: "+f, a...)
			}
		}}
	if tweak != nil {
		tweak(s)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l) }()
	t.Cleanup(func() { _ = l.Close(); <-done })
	return path
}

// TestDarwinHelperVerify is the verifier as a separate process: it prints RESULT=<verdict or error>.
func TestDarwinHelperVerify(t *testing.T) {
	sock := os.Getenv("PROCBIND_DARWIN_SOCK")
	if sock == "" {
		t.Skip("helper process only")
	}
	conn, err := DialUnix(sock)
	if err != nil {
		fmt.Println("RESULT=dial")
		return
	}
	defer conn.Close()
	v, err := dClient().Verify(conn, dToken, dScope)
	if err != nil {
		fmt.Println("RESULT=error")
		return
	}
	fmt.Println("RESULT=" + string(v))
}

// verifyFrom runs the verifier as a child of this process (so a descendant of it), through sh when wrap is set.
func verifyFrom(t *testing.T, sock string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperVerify$", "-test.v")
	cmd.Env = append(os.Environ(), "PROCBIND_DARWIN_SOCK="+sock)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "RESULT="); ok {
			return v
		}
	}
	t.Fatalf("no RESULT in %s", out)
	return ""
}

func TestDarwinServiceAllowsAProcessBelowTheWorker(t *testing.T) {
	self, err := NewProcHandle(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	sock := startDServer(t, &dReg{h: self}, nil)
	if got := verifyFrom(t, sock); got != "allow" {
		t.Fatalf("a child of the worker: %s", got)
	}
}

func TestDarwinServiceDeniesAProcessNotBelowTheWorker(t *testing.T) {
	other := startSleep(t)
	h, err := NewProcHandle(other.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	sock := startDServer(t, &dReg{h: h}, nil)
	if got := verifyFrom(t, sock); got != "deny" {
		t.Fatalf("a process that is not below the worker: %s", got)
	}
}

func TestDarwinServiceDeniesAWorkerThatHasExited(t *testing.T) {
	cmd := startSleep(t)
	h, err := NewProcHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	sock := startDServer(t, &dReg{h: h}, nil)
	if got := verifyFrom(t, sock); got != "deny" {
		t.Fatalf("a worker that has exited: %s", got)
	}
}

// A peer of another uid is closed on without an answer (LOCAL_PEERCRED is the only source of the uid).
func TestDarwinServiceRefusesAPeerOfAnotherUID(t *testing.T) {
	self, _ := NewProcHandle(os.Getpid())
	sock := startDServer(t, &dReg{h: self}, func(s *Server) { s.UID = os.Getuid() + 1 })
	if got := verifyFrom(t, sock); got != "error" {
		t.Fatalf("a peer of another uid got %q, want no answer", got)
	}
}

// A peer the kernel will not describe is not served: no credentials, no answer.
func TestDarwinServiceRefusesAPeerWhoseCredentialsCannotBeRead(t *testing.T) {
	self, _ := NewProcHandle(os.Getpid())
	for name, lookup := range map[string]func(*net.UnixConn) (peerCred, int, error){
		"error":  func(*net.UnixConn) (peerCred, int, error) { return peerCred{}, -1, errors.New("no credentials") },
		"no pid": func(*net.UnixConn) (peerCred, int, error) { return peerCred{Pid: 0, Uid: uint32(os.Getuid())}, -1, nil },
		"pid 1":  func(*net.UnixConn) (peerCred, int, error) { return peerCred{Pid: 1, Uid: uint32(os.Getuid())}, -1, nil },
		"gone pid": func(*net.UnixConn) (peerCred, int, error) {
			return peerCred{Pid: 99998, Uid: uint32(os.Getuid())}, -1, nil
		},
		"someone else": func(*net.UnixConn) (peerCred, int, error) {
			return peerCred{Pid: int32(os.Getppid()), Uid: uint32(os.Getuid())}, -1, nil
		},
	} {
		sock := startDServer(t, &dReg{h: self}, func(s *Server) { s.peerOf = lookup })
		got := verifyFrom(t, sock)
		if name == "someone else" { // a real, readable process that is not the caller: served, but it is the walk that decides
			if got == "error" {
				t.Errorf("%s: a live peer was refused", name)
			}
			continue
		}
		if got != "error" {
			t.Errorf("%s: got %q, want no answer", name, got)
		}
	}
}

// peer reads the connecting process, not the listener.
func TestDarwinPeerNamesTheConnectingProcess(t *testing.T) {
	sock := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd := exec.Command("nc", "-U", sock)
	in, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	c, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cred, fd, err := peer(c)
	if err != nil || fd != -1 {
		t.Fatalf("peer: %v fd=%d", err, fd)
	}
	if int(cred.Pid) != cmd.Process.Pid || int(cred.Uid) != os.Getuid() {
		t.Fatalf("peer = pid %d uid %d; want pid %d uid %d", cred.Pid, cred.Uid, cmd.Process.Pid, os.Getuid())
	}
	if err := checkPeerUID(c, os.Getuid()); err != nil {
		t.Errorf("checkPeerUID: %v", err)
	}
	if err := checkPeerUID(c, os.Getuid()+1); err == nil {
		t.Error("checkPeerUID accepted another uid")
	}
	if k, err := peerIdentity(c, peer); err != nil || k.pid != cmd.Process.Pid || k.start == 0 {
		t.Errorf("peerIdentity = %+v, %v", k, err)
	}
}

func TestDarwinReadyAndPeerSupport(t *testing.T) {
	if err := Ready(); err != nil {
		t.Fatalf("Ready on macOS: %v", err)
	}
	if !PeerPidfdSupported() {
		t.Error("LOCAL_PEERPID is not supported")
	}
}

// --- the checked socket directory ---

func secureOpts(t *testing.T) SocketOptions {
	t.Helper()
	root := shortDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return SocketOptions{Root: root, Profile: "p"}
}

func TestDarwinListenSecureAndDialSecure(t *testing.T) {
	o := secureOpts(t)
	l, err := ListenSecure(o)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	sock := filepath.Join(o.Root, runDirName, "p", sockName)
	fi, err := os.Lstat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket %v %v", fi, err)
	}
	if _, err := ListenSecure(o); !errors.Is(err, ErrAnotherHost) {
		t.Errorf("a second host on the same directory: %v", err)
	}
	go func() {
		c, err := l.AcceptUnix()
		if err == nil {
			_ = c.Close()
		}
	}()
	c, err := DialSecure(o)
	if err != nil {
		t.Fatalf("DialSecure: %v", err)
	}
	_ = c.Close()
	_ = l.Close()
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the listener: %v", err)
	}
	if _, err := DialSecure(o); err == nil {
		t.Error("dialed a service that is gone")
	}
}

func TestDarwinListenSecureRefusesWhatIsNotAsExpected(t *testing.T) {
	// a directory that is open to others
	o := secureOpts(t)
	if err := os.MkdirAll(filepath.Join(o.Root, runDirName, "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(o.Root, runDirName, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if l, err := ListenSecure(o); !errors.Is(err, errUnsafeDir) {
		if l != nil {
			l.Close()
		}
		t.Errorf("a directory with mode 0755: %v", err)
	}
	// a symlink where the directory should be
	o = secureOpts(t)
	elsewhere := shortDir(t)
	if err := os.Symlink(elsewhere, filepath.Join(o.Root, runDirName)); err != nil {
		t.Fatal(err)
	}
	if l, err := ListenSecure(o); !errors.Is(err, errUnsafeDir) {
		if l != nil {
			l.Close()
		}
		t.Errorf("a symlinked directory: %v", err)
	}
	if _, err := DialSecure(o); !errors.Is(err, errUnsafeDir) {
		t.Errorf("DialSecure through a symlinked directory: %v", err)
	}
	// a root that is open to others
	o = secureOpts(t)
	if err := os.Chmod(o.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if l, err := ListenSecure(o); !errors.Is(err, errUnsafeDir) {
		if l != nil {
			l.Close()
		}
		t.Errorf("a root with mode 0755: %v", err)
	}
	// something that is not a socket at the socket's name
	o = secureOpts(t)
	dir := filepath.Join(o.Root, runDirName, "p")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sockName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := ListenSecure(o); !errors.Is(err, errUnsafeDir) {
		if l != nil {
			l.Close()
		}
		t.Errorf("a file at the socket's name: %v", err)
	}
	if _, err := DialSecure(o); !errors.Is(err, errUnsafeDir) {
		t.Errorf("DialSecure to a file: %v", err)
	}
}

// A path an address cannot hold is refused, never truncated.
func TestDarwinListenSecureRefusesAPathTooLongForASocket(t *testing.T) {
	o := secureOpts(t)
	o.Profile = strings.Repeat("a", 60)
	o.Root = filepath.Join(o.Root, strings.Repeat("r", 30))
	if err := os.Mkdir(o.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := ListenSecure(o)
	if err == nil {
		l.Close()
		t.Fatal("a socket path of more than 103 bytes was accepted")
	}
	if !errors.Is(err, errUnsafeDir) {
		t.Errorf("error %v", err)
	}
}

func TestDarwinOpenBelowTakesOnePlainName(t *testing.T) {
	dir := shortDir(t)
	fd, err := syscall.Open(dir, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "a\x00b"} {
		if got, err := openBelow(fd, bad, syscall.O_RDONLY, 0); err == nil {
			syscall.Close(got)
			t.Errorf("openBelow accepted %q", bad)
		}
	}
	if err := os.Symlink("/etc", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if got, err := openBelow(fd, "link", syscall.O_RDONLY, 0); err == nil {
		syscall.Close(got)
		t.Error("openBelow followed a symlink")
	}
}

// --- stopping a turn's leader ---

func startGroupLeader(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	// the group has a second member once the shell has started its background job
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if out, _ := exec.Command("pgrep", "-g", itoa(cmd.Process.Pid)).Output(); len(strings.Fields(string(out))) >= 2 {
			return cmd
		}
	}
	t.Fatal("the shell started no background job")
	return cmd
}

func stopper() Stopper {
	return Stopper{StampOf: procchain.BootStamp, Same: procchain.SameStamp, Grace: 3 * time.Second}
}

func TestDarwinStopLeaderKillsTheRecordedGroup(t *testing.T) {
	cmd := startGroupLeader(t)
	pid := cmd.Process.Pid
	stamp := procchain.BootStamp(pid)
	if stamp == "" {
		t.Fatal("no stamp")
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	if r := stopper().StopLeader(pid, stamp); r.Outcome != StopStopped {
		t.Fatalf("StopLeader = %+v", r)
	}
	<-waited
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(-pid, 0); err == nil {
		t.Error("the leader's group still has a member")
	}
}

// A number held by a process with another start stamp is not signalled: it is not the recorded leader.
func TestDarwinStopLeaderDoesNotSignalAnotherIncarnation(t *testing.T) {
	cmd := startGroupLeader(t)
	signalled := false
	s := stopper()
	s.kill = func(int, unix.Signal) error { signalled = true; return nil }
	if r := s.StopLeader(cmd.Process.Pid, "start:1@"+strings.SplitN(procchain.BootStamp(cmd.Process.Pid), "@", 2)[1]); r.Outcome != StopAbsent {
		t.Fatalf("a stamp of another incarnation: %+v", r)
	}
	if signalled {
		t.Fatal("a process with another start stamp was signalled")
	}
}

func TestDarwinStopLeaderHoldsWhatItCannotIdentify(t *testing.T) {
	cmd := startGroupLeader(t)
	pid := cmd.Process.Pid
	for name, tc := range map[string]struct {
		pid   int
		stamp string
		of    func(int) string
	}{
		"no stamp recorded": {pid, "", procchain.BootStamp},
		"pid 1":             {1, "start:1", procchain.BootStamp},
		"unreadable stamp":  {pid, "start:1", func(int) string { return "" }},
		"no stamp reader":   {pid, "start:1", nil},
	} {
		s := stopper()
		s.StampOf = tc.of
		s.kill = func(int, unix.Signal) error { t.Errorf("%s: signalled", name); return nil }
		if r := s.StopLeader(tc.pid, tc.stamp); r.Outcome != StopHeld {
			t.Errorf("%s: %+v, want held", name, r)
		}
	}
}

func TestDarwinStopLeaderOfAProcessThatIsGone(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if r := stopper().StopLeader(cmd.Process.Pid, "start:5@x"); r.Outcome != StopAbsent {
		t.Errorf("StopLeader of a gone process = %+v", r)
	}
}
