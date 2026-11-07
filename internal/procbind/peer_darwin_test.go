//go:build darwin

package procbind

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The connector is identified by what the kernel attached to the connection, through the real peer() and the real
// accept path. A pid that is recycled cannot be staged, but an exec gives the same process a new pid version while its
// pid and start time stay: exactly the identity the audit token carries and the pid alone does not.

// TestDarwinHelperExecAfterConnect connects, hands the connection to a new image of itself (exec keeps the pid and the
// start time but not the pid version), tells the test, and verifies from there.
func TestDarwinHelperExecAfterConnect(t *testing.T) {
	sock := os.Getenv("PROCBIND_DARWIN_EXEC_SOCK")
	if sock == "" {
		t.Skip("helper process only")
	}
	if fdStr := os.Getenv("PROCBIND_DARWIN_EXEC_FD"); fdStr != "" { // after the exec
		var fd int
		fmt.Sscan(fdStr, &fd)
		f := os.NewFile(uintptr(fd), "conn")
		conn, err := net.FileConn(f)
		if err != nil {
			fmt.Println("RESULT=fileconn")
			return
		}
		fmt.Println("EXECED")
		v, err := dClient().Verify(conn, dToken, dScope)
		if err != nil {
			fmt.Println("RESULT=error")
			return
		}
		fmt.Println("RESULT=" + string(v))
		return
	}
	conn, err := DialUnix(sock)
	if err != nil {
		fmt.Println("RESULT=dial")
		return
	}
	rc, _ := conn.SyscallConn()
	var fd int
	_ = rc.Control(func(f uintptr) {
		d, _ := unix.FcntlInt(f, unix.F_DUPFD, 10) // a plain duplicate: no close-on-exec
		fd = d
	})
	env := append(os.Environ(), fmt.Sprintf("PROCBIND_DARWIN_EXEC_FD=%d", fd))
	_ = syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestDarwinHelperExecAfterConnect$", "-test.v"}, env)
	fmt.Println("RESULT=exec")
}

func TestDarwinConnectorThatBecameAnotherImageIsNotServed(t *testing.T) {
	self, _ := NewProcHandle(os.Getpid())
	dir := shortDir(t)
	path := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperExecAfterConnect$", "-test.v")
	cmd.Env = append(os.Environ(), "PROCBIND_DARWIN_EXEC_SOCK="+path)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		var acc string
		for {
			n, err := out.Read(buf)
			acc += string(buf[:n])
			for {
				i := strings.IndexByte(acc, '\n')
				if i < 0 {
					break
				}
				lines <- acc[:i]
				acc = acc[i+1:]
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	// the connection sits in the backlog until the helper is a different image; only then is it accepted
	waitLine(t, lines, "EXECED")
	procs, _ := NewProcs()
	s := &Server{Profile: dProfile, Generation: dGen, Key: dKey, Registry: &dReg{h: self}, Procs: procs, UID: os.Getuid(), Timeout: 3 * time.Second,
		Logf: func(f string, a ...any) { t.Logf("server: "+f, a...) }}
	go func() { _ = s.Serve(l) }()
	if got := waitLine(t, lines, "RESULT="); got != "RESULT=error" {
		t.Fatalf("a connector that is another image than the one that connected got %q, want no answer", got)
	}
}

func waitLine(t *testing.T, lines chan string, prefix string) string {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("helper ended without %q", prefix)
			}
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-deadline:
			t.Fatalf("no %q from the helper", prefix)
		}
	}
}

// A connector that exited before the accept leaves nothing to pin: the connection is refused.
func TestDarwinConnectorThatExitedBeforeAcceptIsRefused(t *testing.T) {
	path := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cmd := exec.Command("nc", "-U", path)
	in, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // connected, in the backlog
	_ = in.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	c, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cred, fd, perr := peer(c)
	if perr == nil {
		if _, herr := peerHandle(fd, cred); herr == nil {
			t.Fatalf("the exited connector (pid %d) was pinned", cred.Pid)
		}
	}
	if _, err := peerIdentity(c, peer); err == nil {
		t.Error("peerIdentity named a connector that had exited")
	}
}

// peer() on a socket with no peer is an error, not a zero credential.
func TestDarwinPeerOfAnUnconnectedSocketIsAnError(t *testing.T) {
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: filepath.Join(shortDir(t), "d"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cred, fd, err := peer(c)
	if err == nil {
		t.Fatalf("peer() of an unconnected socket = %+v", cred)
	}
	if fd != -1 {
		t.Errorf("fd = %d", fd)
	}
	if err := checkPeerUID(c, os.Getuid()); err == nil {
		t.Error("checkPeerUID of an unconnected socket passed")
	}
}
