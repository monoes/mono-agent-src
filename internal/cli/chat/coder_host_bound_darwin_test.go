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
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatal("a process whose pinned pair was gone was signalled")
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
