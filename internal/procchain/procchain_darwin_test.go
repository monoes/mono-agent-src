//go:build darwin

package procchain

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The macOS reader answers from the kernel's process table: these run against the real one.

func TestDarwinReadsAncestryOfThisProcess(t *testing.T) {
	if !Supported {
		t.Fatal("ancestry must be read on macOS: Supported is false")
	}
	self := os.Getpid()
	if st := Stamp(self); !strings.HasPrefix(st, "start:") || st == "start:" {
		t.Fatalf("stamp of this process = %q", st)
	}
	if Stamp(self) != Stamp(self) {
		t.Error("the stamp of a live process is not stable")
	}
	chain, ok := Chain()
	if len(chain) < 2 || chain[0] != self || chain[1] != os.Getppid() {
		t.Fatalf("chain %v (ok=%v) does not run from this process to its parent %d", chain, ok, os.Getppid())
	}
}

func TestDarwinParentOfAChildIsThisProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if p, ok := parentPID(cmd.Process.Pid); !ok || p != os.Getpid() {
		t.Fatalf("parent of the child = %d,%v; want %d", p, ok, os.Getpid())
	}
	st := Stamp(cmd.Process.Pid)
	if st == "" || st == Stamp(os.Getpid()) {
		t.Fatalf("child stamp %q", st)
	}
	if !InChain([]int{cmd.Process.Pid, os.Getpid()}, cmd.Process.Pid, st) {
		t.Error("the child is not in its own chain")
	}
}

// A pid whose process is gone, or is another incarnation, reads as nothing: no stamp, no parent.
func TestDarwinGoneProcessReadsAsNothing(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if st := Stamp(cmd.Process.Pid); st != "" {
		t.Errorf("stamp of a reaped process = %q", st)
	}
	if _, ok := parentPID(cmd.Process.Pid); ok {
		t.Error("parent of a reaped process was read")
	}
	if Stamp(0) != "" || Stamp(-1) != "" {
		t.Error("a stamp was read for pid 0 or -1")
	}
}

// A child whose parent exited is read as a child of launchd (pid 1), where a chain ends.
func TestDarwinAnOrphanIsReadAsChildOfLaunchd(t *testing.T) {
	out, err := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("kill", strconv.Itoa(pid)).Run() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := parentPID(pid); ok && p == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p, ok := parentPID(pid); !ok || p != 1 {
		t.Fatalf("orphan parent = %d,%v; want 1", p, ok)
	}
}

func TestDarwinBootStampCarriesTheBoot(t *testing.T) {
	st := BootStamp(os.Getpid())
	if !strings.Contains(st, "@") {
		t.Fatalf("boot stamp %q has no boot id", st)
	}
	if !SameStamp(Stamp(os.Getpid()), st) {
		t.Error("the boot stamp of this process does not match its plain stamp")
	}
}

// A process that has exited but not been waited for is a zombie: it has no stamp, so it is not "in" anything.
func TestDarwinZombieHasNoStamp(t *testing.T) {
	cmd := exec.Command("sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	if Stamp(cmd.Process.Pid) == "" {
		t.Fatal("a running process has no stamp")
	}
	deadline := time.Now().Add(5 * time.Second)
	for Stamp(cmd.Process.Pid) != "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := Stamp(cmd.Process.Pid); st != "" {
		t.Errorf("an exited, unreaped process still has the stamp %q", st)
	}
}

func TestDarwinEntryOfATableEntry(t *testing.T) {
	var k unix.KinfoProc
	k.Proc.P_starttime = unix.Timeval{Sec: 5, Usec: 3}
	k.Eproc.Ppid = 42
	if e, ok := EntryOf(&k); !ok || e.Ppid != 42 || e.Start != 5_000_003 || e.Zombie {
		t.Fatalf("good: %+v %v", e, ok)
	}
	if _, ok := EntryOf(nil); ok {
		t.Error("a missing entry was read")
	}
	z := k
	z.Proc.P_stat = stateZombie
	if e, ok := EntryOf(&z); !ok || !e.Zombie {
		t.Errorf("a zombie is not marked: %+v %v", e, ok)
	}
	for name, tv := range map[string]unix.Timeval{"zero": {}, "negative seconds": {Sec: -1, Usec: 5}, "negative microseconds": {Sec: 5, Usec: -5}, "microseconds past a second": {Sec: 5, Usec: 1_000_000}, "no seconds": {Sec: 0, Usec: 5}} {
		bad := k
		bad.Proc.P_starttime = tv
		if _, ok := EntryOf(&bad); ok {
			t.Errorf("an entry with a %s start time was read", name)
		}
	}
}
