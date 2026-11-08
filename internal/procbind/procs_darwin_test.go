//go:build darwin

package procbind

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These run against the real macOS process table.

func startSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestDarwinHandleNamesOneIncarnation(t *testing.T) {
	cmd := startSleep(t)
	h, err := NewProcHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Alive() {
		t.Fatal("a running process is not alive")
	}
	if pid, err := h.Pid(); err != nil || pid != cmd.Process.Pid {
		t.Fatalf("Pid = %d, %v", pid, err)
	}
	_ = cmd.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for h.Alive() && time.Now().Before(deadline) { // killed but not yet waited for: a zombie is not alive
		time.Sleep(5 * time.Millisecond)
	}
	if h.Alive() {
		t.Fatal("a killed process is still alive")
	}
	_ = cmd.Wait()
	if h.Alive() {
		t.Error("a reaped process is alive")
	}
	if _, err := h.Pid(); err == nil {
		t.Error("Pid answered for a process that is gone")
	}
	if _, err := NewProcHandle(cmd.Process.Pid); err == nil {
		t.Error("a handle was made for a pid that is gone")
	}
}

// A number that now belongs to another process (another start time) is not the pinned process.
func TestDarwinHandleWithAnotherStartTimeIsNotAlive(t *testing.T) {
	h, err := NewProcHandle(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	forged := &procHandle{pid: h.(*procHandle).pid, start: h.(*procHandle).start + 1}
	if forged.Alive() {
		t.Error("a handle with another start time is alive")
	}
	if _, err := forged.Pid(); err == nil {
		t.Error("Pid answered for a handle with another start time")
	}
	p := DarwinProcs{}
	if _, err := p.Parent(forged); err == nil {
		t.Error("Parent answered for a handle with another start time")
	}
	if _, err := p.Start(forged); err == nil {
		t.Error("Start answered for a handle with another start time")
	}
}

func TestDarwinProcsReadParentAndStart(t *testing.T) {
	p, err := NewProcs()
	if err != nil {
		t.Fatal(err)
	}
	cmd := startSleep(t)
	h, err := p.Open(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if parent, err := p.Parent(h); err != nil || parent != os.Getpid() {
		t.Fatalf("parent = %d, %v; want %d", parent, err, os.Getpid())
	}
	self, _ := p.Open(os.Getpid())
	cs, err1 := p.Start(h)
	ss, err2 := p.Start(self)
	if err1 != nil || err2 != nil || cs < ss || cs == 0 {
		t.Fatalf("start times child=%d (%v) self=%d (%v): a child cannot start before its parent", cs, err1, ss, err2)
	}
	if _, err := p.Open(0); err == nil {
		t.Error("pid 0 was opened")
	}
}

// The walk, on the real table: the worker is a shell, the caller is a process below it.
func TestDarwinWalkAllowsAnAncestorAndRefusesTheRest(t *testing.T) {
	p, _ := NewProcs()
	sh := exec.Command("sh", "-c", "sleep 60 & wait")
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sh.Process.Kill(); _ = sh.Wait() })
	var kid int
	for deadline := time.Now().Add(5 * time.Second); kid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		out, _ := exec.Command("pgrep", "-P", itoa(sh.Process.Pid)).Output()
		kid = atoi(string(out))
	}
	if kid == 0 {
		t.Fatal("the shell started no child")
	}
	worker, err := p.Open(sh.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := p.Open(kid)
	if err != nil {
		t.Fatal(err)
	}
	if r := Walk(p, caller, worker); !r.Allowed {
		t.Fatalf("a child of the worker was refused: %s", r)
	}
	if r := Walk(p, worker, caller); r.Allowed {
		t.Error("the worker was allowed as a descendant of its own child")
	}
	other := startSleep(t)
	stranger, _ := p.Open(other.Process.Pid)
	if r := Walk(p, stranger, worker); r.Allowed {
		t.Error("an unrelated process was allowed")
	}
	_ = sh.Process.Kill()
	_ = sh.Wait()
	if r := Walk(p, caller, worker); r.Allowed {
		t.Error("a walk to a worker that has exited was allowed")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(s, "\n", 2)[0]))
	return n
}
