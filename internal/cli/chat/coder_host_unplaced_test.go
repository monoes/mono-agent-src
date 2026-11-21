//go:build linux || darwin

package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The child of a bound launch must not outlive its host while it is still unplaced. macOS has no parent-death signal, and a child
// cannot be started stopped from Go, so the mechanism is the one the launch already has: the child's first act is to read its
// credential frame from a pipe whose write end only the host holds (close-on-exec, kept by nobody else), and an end of file, which a
// dead host causes at once, is fatal (coderhost.StashCredFrame). These tests pin that mechanism on the real kernel: a host killed with
// SIGKILL between the start and the adoption leaves a child that exits by itself, at once and for that reason, and a host that is
// alive leaves it waiting.

const unplacedRole = "S3_UNPLACED_ROLE"

// TestHelperUnplacedHost is the host of the test below: it starts a bound child exactly as the launcher does and then hangs, before
// any adoption or delivery.
func TestHelperUnplacedHost(t *testing.T) {
	if os.Getenv(unplacedRole) != "host" {
		t.Skip("helper process only")
	}
	dir := os.Getenv("S3_UNPLACED_DIR")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperUnplacedChild$")
	cmd.Env = append(os.Environ(), unplacedRole+"=child")
	d, err := (&boundLaunch{}).attach(cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.start(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	select {} // killed by the test
}

// TestHelperUnplacedChild is the child: the turn's first act, reading the credential frame.
func TestHelperUnplacedChild(t *testing.T) {
	if os.Getenv(unplacedRole) != "child" {
		t.Skip("helper process only")
	}
	start := time.Now()
	err := coderhost.StashCredFrame()
	msg := "ok"
	if err != nil {
		msg = "error: " + err.Error()
	}
	_ = os.WriteFile(filepath.Join(os.Getenv("S3_UNPLACED_DIR"), "child.result"), []byte(msg+" after "+time.Since(start).String()), 0o600)
}

func TestAHostKilledBetweenStartAndAdoptLeavesNoRunningChild(t *testing.T) {
	dir := t.TempDir()
	host := exec.Command(os.Args[0], "-test.run=^TestHelperUnplacedHost$")
	host.Env = append(os.Environ(), unplacedRole+"=host", "S3_UNPLACED_DIR="+dir)
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	hostDone := make(chan struct{})
	go func() { _ = host.Wait(); close(hostDone) }()
	var childPID int
	t.Cleanup(func() {
		_ = host.Process.Kill()
		<-hostDone
		if childPID > 1 && syscall.Kill(childPID, 0) == nil {
			_ = syscall.Kill(childPID, syscall.SIGKILL) // our own helper child, by the pid it reported
		}
	})
	var raw []byte
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(filepath.Join(dir, "child.pid")); err == nil && len(b) > 0 {
			raw = b
			break
		}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("the host never started its child: %q", raw)
	}
	childPID = pid
	if sid, err := unix.Getsid(pid); err != nil || sid != pid {
		t.Fatalf("the child is not a session leader of its own (sid %d, %v)", sid, err)
	}
	// control: while the host lives and has not delivered, the child waits (it did not leave for some other reason)
	time.Sleep(700 * time.Millisecond)
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the child left before its host died")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "child.result")); len(b) != 0 {
		t.Fatalf("the child ended while its host was alive: %s", b)
	}
	// the host dies between Start and Adopt
	killed := time.Now()
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-hostDone
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err != nil {
			break
		}
		if processState(pid) == "Z" { // a zombie of init is gone as far as running goes
			break
		}
	}
	if syscall.Kill(pid, 0) == nil && processState(pid) != "Z" {
		t.Fatalf("the unplaced child outlived its host (%s later): it runs without being in any unit", time.Since(killed))
	}
	if took := time.Since(killed); took > 5*time.Second {
		t.Errorf("the child took %s to leave: it must not have waited for the frame's own timeout", took)
	}
	res := ""
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end) && res == ""; time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Join(dir, "child.result"))
		res = string(b)
	}
	if !strings.HasPrefix(res, "error: ") || !strings.Contains(res, "did not parse") {
		t.Errorf("the child left for another reason than the end of its pipe: %q", res)
	}
}
