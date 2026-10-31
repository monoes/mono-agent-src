//go:build linux

package procbind

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func stampFor(pid int) string { return "stamp-" + strconv.Itoa(pid) }

func startChild(t *testing.T, script string) (pid int, done <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ch := make(chan struct{})
	go func() { _ = cmd.Wait(); close(ch) }()
	t.Cleanup(func() { _ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); <-ch })
	return cmd.Process.Pid, ch
}

func testStopper() Stopper {
	return Stopper{StampOf: stampFor, Grace: 300 * time.Millisecond}
}

func exited(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

func TestStopLeaderStopsTheRecordedLeaderThroughItsPidfd(t *testing.T) {
	pid, done := startChild(t, "sleep 60")
	r := testStopper().StopLeader(pid, stampFor(pid))
	if r.Outcome != StopStopped || !exited(done) {
		t.Fatalf("%+v exited=%v", r, exited(done))
	}
}

// A leader that ignores SIGTERM gets SIGKILL after the grace.
func TestStopLeaderEscalatesToKill(t *testing.T) {
	pid, done := startChild(t, `trap "" TERM; while :; do sleep 1; done`)
	time.Sleep(150 * time.Millisecond) // let the trap be set
	r := testStopper().StopLeader(pid, stampFor(pid))
	if r.Outcome != StopStopped || !exited(done) {
		t.Fatalf("%+v", r)
	}
}

// Confirmed absent: a process that is gone, and a number that now belongs to another process (other stamp): nothing is
// signalled in either case.
func TestStopLeaderConfirmsAbsenceWithoutSignalling(t *testing.T) {
	pid, done := startChild(t, "exit 0")
	if !exited(done) {
		t.Fatal("the child did not exit")
	}
	if r := testStopper().StopLeader(pid, stampFor(pid)); r.Outcome != StopAbsent {
		t.Errorf("a process that is gone: %+v", r)
	}
	other, odone := startChild(t, "sleep 60")
	if r := testStopper().StopLeader(other, "the-stamp-of-another-process"); r.Outcome != StopAbsent {
		t.Errorf("another process on the number: %+v", r)
	}
	select {
	case <-odone:
		t.Error("a process that was not the recorded leader was signalled")
	case <-time.After(300 * time.Millisecond):
	}
}

// Unable to check is held, never absent, and nothing is signalled: no descriptor, no permission, an unreadable stamp,
// a stamp that cannot be read the second time, a signal that is refused, and a leader that will not die.
func TestStopLeaderHoldsWhenItCannotCheck(t *testing.T) {
	pid, done := startChild(t, "sleep 60")
	alive := func() bool {
		select {
		case <-done:
			return false
		case <-time.After(200 * time.Millisecond):
			return true
		}
	}
	cases := map[string]Stopper{
		"out of descriptors":     {StampOf: stampFor, openPidfd: func(int) (int, error) { return -1, unix.EMFILE }},
		"too many open":          {StampOf: stampFor, openPidfd: func(int) (int, error) { return -1, unix.ENFILE }},
		"no permission":          {StampOf: stampFor, openPidfd: func(int) (int, error) { return -1, unix.EPERM }},
		"no memory":              {StampOf: stampFor, openPidfd: func(int) (int, error) { return -1, unix.ENOMEM }},
		"unreadable stamp":       {StampOf: func(int) string { return "" }},
		"no stamp function":      {},
		"signal refused":         {StampOf: stampFor, Grace: 100 * time.Millisecond, signal: func(int, unix.Signal) error { return unix.EPERM }},
		"signals have no effect": {StampOf: stampFor, Grace: 100 * time.Millisecond, signal: func(int, unix.Signal) error { return nil }},
	}
	for name, s := range cases {
		r := s.StopLeader(pid, stampFor(pid))
		if r.Outcome != StopHeld || r.Cause == "" {
			t.Errorf("%s: %+v, want held with a cause", name, r)
		}
		if strings.Contains(r.Cause, "/") {
			t.Errorf("%s: the cause names a path: %q", name, r.Cause)
		}
		if !alive() {
			t.Fatalf("%s: the leader was killed although the check could not be completed", name)
		}
	}
	// no recorded identity at all
	for _, c := range []struct {
		pid   int
		stamp string
	}{{0, "s"}, {1, "s"}, {pid, ""}} {
		if r := testStopper().StopLeader(c.pid, c.stamp); r.Outcome != StopHeld {
			t.Errorf("identity (%d, %q): %+v", c.pid, c.stamp, r)
		}
	}
}
