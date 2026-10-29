//go:build linux

package procbind

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func skipWithoutPidfd(t *testing.T) {
	t.Helper()
	h, err := OpenPidfd(os.Getpid())
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			t.Skipf("pidfd_open is not available here: %v", err)
		}
		t.Fatal(err)
	}
	_ = h.Close()
}

// shTree starts `sh` with one `sleep` child and returns both pids; both are killed and reaped when
// the test ends.
func shTree(t *testing.T) (sh *exec.Cmd, sleepPid int) {
	t.Helper()
	sh = exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	out, err := sh.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	sleepPid, err = strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(sleepPid, syscall.SIGKILL)
		_ = sh.Process.Kill()
		_ = sh.Wait()
	})
	return sh, sleepPid
}

func open(t *testing.T, pid int) Handle {
	t.Helper()
	h, err := OpenPidfd(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestLinuxHandleReadsTheRealProcessTable(t *testing.T) {
	skipWithoutPidfd(t)
	sh, sleepPid := shTree(t)
	h := open(t, sh.Process.Pid)
	if !h.Alive() {
		t.Fatal("a running process is not alive")
	}
	if pid, err := h.Pid(); err != nil || pid != sh.Process.Pid {
		t.Errorf("Pid = %d, %v; want %d", pid, err, sh.Process.Pid)
	}
	var p LinuxProcs
	if ppid, err := p.Parent(h); err != nil || ppid != os.Getpid() {
		t.Errorf("Parent = %d, %v; want %d", ppid, err, os.Getpid())
	}
	if st, err := p.Start(h); err != nil || st == 0 {
		t.Errorf("Start = %d, %v", st, err)
	}
	child := open(t, sleepPid)
	if ppid, err := p.Parent(child); err != nil || ppid != sh.Process.Pid {
		t.Errorf("child Parent = %d, %v; want %d", ppid, err, sh.Process.Pid)
	}
	// the child cannot have started before its parent
	a, _ := p.Start(h)
	b, _ := p.Start(child)
	if b < a {
		t.Errorf("child start %d before parent start %d", b, a)
	}
	// a process that has exited is not alive, and its number is no longer readable once reaped
	_ = sh.Process.Kill()
	_ = sh.Wait()
	if h.Alive() {
		t.Error("an exited and reaped process is still alive")
	}
	if _, err := h.Pid(); err == nil {
		t.Error("the number of a reaped process was still readable")
	}
}

func TestLinuxWalkOnRealProcesses(t *testing.T) {
	skipWithoutPidfd(t)
	sh, sleepPid := shTree(t)
	worker := open(t, sh.Process.Pid)
	child := open(t, sleepPid)

	if r := Walk(LinuxProcs{}, child, worker); !r.Allowed {
		t.Errorf("a child of the worker: %v", r)
	}
	if r := Walk(LinuxProcs{}, worker, worker); !r.Allowed {
		t.Errorf("the worker itself: %v", r)
	}

	// a process of the same user that is not under the worker
	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	if r := Walk(LinuxProcs{}, open(t, other.Process.Pid), worker); r.Allowed {
		t.Errorf("a sibling of the worker: %v", r)
	}
	// the worker is not a descendant of its own child
	if r := Walk(LinuxProcs{}, worker, child); r.Allowed {
		t.Errorf("the parent walking to its child: %v", r)
	}

	// the caller exits: its handle is not alive
	if err := syscall.Kill(sleepPid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitDead(t, child)
	if r := Walk(LinuxProcs{}, child, worker); r.Allowed || r.Reason != ReasonExited {
		t.Errorf("an exited caller: %v", r)
	}
}

func TestLinuxWalkDeniesAWorkerThatExited(t *testing.T) {
	skipWithoutPidfd(t)
	sh, sleepPid := shTree(t)
	worker := open(t, sh.Process.Pid)
	child := open(t, sleepPid)
	_ = sh.Process.Kill()
	_ = sh.Wait()
	// the child is reparented away from the worker, which is gone
	if r := Walk(LinuxProcs{}, child, worker); r.Allowed {
		t.Errorf("a child of an exited worker: %v", r)
	}
}

func waitDead(t *testing.T, h Handle) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if !h.Alive() {
			return
		}
		unix.Nanosleep(&unix.Timespec{Nsec: 5_000_000}, nil)
	}
	t.Fatal("process did not exit")
}
