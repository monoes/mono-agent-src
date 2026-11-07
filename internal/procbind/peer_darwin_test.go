//go:build darwin

package procbind

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The peer is the process that holds the peer's end of THIS connection, proved from the kernel's tables through the real
// peer() and the real accept path. A recycled pid cannot be staged, but its effect can: the process at the peer's pid
// (the connector, standing for the recycled process) does not hold the connection because it handed it to another
// process and let go.

// TestDarwinHelperHandoff connects, starts a child that inherits the connection and verifies through it, and closes its own
// end. The child prints RESULT=; this process keeps the peer's pid but no longer holds the connection.
func TestDarwinHelperHandoff(t *testing.T) {
	sock := os.Getenv("PROCBIND_DARWIN_HANDOFF_SOCK")
	if sock == "" {
		t.Skip("helper process only")
	}
	conn, err := DialUnix(sock)
	if err != nil {
		fmt.Println("RESULT=dial")
		return
	}
	f, _ := conn.File() // a duplicate for the child
	child := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperHolder$", "-test.v")
	child.Env = append(os.Environ(), "PROCBIND_DARWIN_HOLDER=1")
	child.ExtraFiles = []*os.File{f}
	child.Stdout = os.Stdout
	if err := child.Start(); err != nil {
		fmt.Println("RESULT=start")
		return
	}
	_ = f.Close()
	_ = conn.Close() // this process, the one the kernel names as the peer, lets go of the connection
	_ = child.Wait()
}

func TestDarwinHelperHolder(t *testing.T) {
	if os.Getenv("PROCBIND_DARWIN_HOLDER") == "" {
		t.Skip("helper process only")
	}
	conn, err := net.FileConn(os.NewFile(3, "conn"))
	if err != nil {
		fmt.Println("RESULT=fileconn")
		return
	}
	v, err := dClient().Verify(conn, dToken, dScope)
	if err != nil {
		fmt.Println("RESULT=error")
		return
	}
	fmt.Println("RESULT=" + string(v))
}

func TestDarwinConnectorThatLetGoOfTheConnectionIsNotServed(t *testing.T) {
	self, _ := NewProcHandle(os.Getpid())
	sock := startDServer(t, &dReg{h: self}, nil)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinHelperHandoff$", "-test.v")
	cmd.Env = append(os.Environ(), "PROCBIND_DARWIN_HANDOFF_SOCK="+sock)
	out, _ := cmd.CombinedOutput()
	got := ""
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "RESULT="); ok {
			got = v
		}
	}
	if got != "error" {
		t.Fatalf("a connection held by a process other than the one at the peer's pid got %q, want no answer\n%s", got, out)
	}
}

// The process that holds the connection is served as before, through the same path.
func TestDarwinConnectorThatHoldsTheConnectionIsServed(t *testing.T) {
	self, _ := NewProcHandle(os.Getpid())
	sock := startDServer(t, &dReg{h: self}, nil)
	if got := verifyFrom(t, sock); got != "allow" {
		t.Fatalf("got %q", got)
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
		if _, herr := peerHandle(c, fd, cred); herr == nil {
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
