//go:build darwin

package chat

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The bound launch's hold on a turn's process on macOS (pid and start time instead of a pidfd), on a real process.

func startBound(t *testing.T, pre func(*exec.Cmd) error) (*boundDelivery, *exec.Cmd, error) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	d, err := (&boundLaunch{}).attach(cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.close)
	err = d.start(cmd, pre)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return d, cmd, err
}

func exited(cmd *exec.Cmd) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
			return true
		}
		var ws syscall.WaitStatus
		if p, _ := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WNOHANG, nil); p == cmd.Process.Pid {
			return true
		}
	}
	return false
}

func TestDarwinBoundStartPinsAndKillsTheTurnsLeader(t *testing.T) {
	d, cmd, err := startBound(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pgid, _ := syscall.Getpgid(cmd.Process.Pid); pgid != cmd.Process.Pid {
		t.Errorf("the turn is not a session leader (pgid %d, pid %d)", pgid, cmd.Process.Pid)
	}
	h, err := d.proc.handle()
	if err != nil || !h.Alive() {
		t.Fatalf("handle: %v", err)
	}
	if pid, _ := h.Pid(); pid != cmd.Process.Pid {
		t.Errorf("handle names pid %d, want %d", pid, cmd.Process.Pid)
	}
	hooks := 0
	d.setOnKill(func() { hooks++ })
	d.kill()
	if hooks != 1 {
		t.Errorf("onKill ran %d times", hooks)
	}
	if !exited(cmd) {
		t.Fatal("kill did not end the turn's leader")
	}
	if h.Alive() {
		t.Error("the handle of an ended turn is alive")
	}
}

// A hold that no longer names the process (the number was reused) signals nothing.
func TestDarwinBoundKillDoesNotSignalAProcessThatIsNotTheHeldOne(t *testing.T) {
	d, cmd, err := startBound(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.proc.worker = deadHandle{} // the pinned pair no longer names a live process
	d.kill()
	time.Sleep(300 * time.Millisecond)
	var ws syscall.WaitStatus
	if p, _ := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WNOHANG, nil); p != 0 {
		t.Fatalf("a process whose pinned pair was gone was signalled (%v)", ws)
	}
	d.proc.release() // idempotent
	d.proc.release()
	if _, err := d.proc.handle(); err == nil {
		t.Error("a released hold gave a handle")
	}
}

type deadHandle struct{}

func (deadHandle) Alive() bool       { return false }
func (deadHandle) Pid() (int, error) { return 0, syscall.ESRCH }
func (deadHandle) Close() error      { return nil }

func TestDarwinBoundStartRefusesAPlacementThatReplacedTheAttributes(t *testing.T) {
	for name, pre := range map[string]func(*exec.Cmd) error{
		"replaced": func(c *exec.Cmd) error { c.SysProcAttr = &syscall.SysProcAttr{}; return nil },
		"dropped":  func(c *exec.Cmd) error { c.SysProcAttr = nil; return nil },
	} {
		_, cmd, err := startBound(t, pre)
		if err == nil || !strings.Contains(err.Error(), "replaced the launcher's process attributes") {
			t.Errorf("%s: err = %v", name, err)
		}
		if cmd.Process != nil {
			t.Errorf("%s: a process was started", name)
		}
	}
}

// A turn placed in a run's process group (setpgid) is started WITHOUT a session of its own: the kernel refuses both at once
// (EPERM), and a session leader cannot change its group. It stays in the host's session, in the group, and is not the leader
// of anything.
func TestDarwinBoundStartPlacedInAGroupJoinsItWithoutASession(t *testing.T) {
	anchor := exec.Command("sleep", "60")
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := anchor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Process.Kill(); _ = anchor.Wait() })
	join := func(c *exec.Cmd) error {
		c.SysProcAttr.Setpgid, c.SysProcAttr.Pgid = true, anchor.Process.Pid
		return nil
	}
	_, cmd, err := startBound(t, join)
	if err != nil {
		t.Fatalf("a turn placed in a group of this session was not started: %v", err)
	}
	if pg, _ := syscall.Getpgid(cmd.Process.Pid); pg != anchor.Process.Pid {
		t.Errorf("the turn is in group %d, want the anchor's %d", pg, anchor.Process.Pid)
	}
	mine, _ := syscall.Getsid(0)
	if sid, _ := syscall.Getsid(cmd.Process.Pid); sid != mine {
		t.Errorf("the turn is in session %d, want the host's %d", sid, mine)
	}
}

// With both a session and a group the kernel refuses: the old placement could never start a turn (#1582).
func TestDarwinBoundStartSessionAndGroupTogetherIsRefusedByTheKernel(t *testing.T) {
	anchor := exec.Command("sleep", "60")
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := anchor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Process.Kill(); _ = anchor.Wait() })
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setpgid: true, Pgid: anchor.Process.Pid}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("the kernel accepted a session and a group together: the host's placement no longer needs the exception")
	}
}
