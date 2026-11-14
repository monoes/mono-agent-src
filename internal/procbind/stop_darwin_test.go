//go:build darwin

package procbind

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A turn placed in a run's process group (macOS) is a member, not the leader, of that group: stopping it signals that
// process only. Signalling its group would end every other turn of the run, and the run's anchor.
func TestStopLeaderOfAMemberDoesNotSignalTheGroup(t *testing.T) {
	start := func(pgid int) *exec.Cmd {
		c := exec.Command("sleep", "60")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { _ = c.Wait(); close(done) }() // the only Wait of this command
		t.Cleanup(func() { _ = c.Process.Kill(); <-done })
		return c
	}
	anchor := start(0)
	turn, sibling := start(anchor.Process.Pid), start(anchor.Process.Pid)
	res := Stopper{StampOf: func(int) string { return "s" }, Grace: 3 * time.Second}.StopLeader(turn.Process.Pid, "s")
	if res.Outcome != StopStopped {
		t.Fatalf("outcome %v (%s)", res.Outcome, res.Cause)
	}
	for name, c := range map[string]*exec.Cmd{"the run's anchor": anchor, "another turn of the run": sibling} {
		if err := unix.Kill(c.Process.Pid, 0); err != nil {
			t.Errorf("%s was ended by stopping one turn: %v", name, err)
		}
	}
}
