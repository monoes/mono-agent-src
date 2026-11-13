//go:build !windows

package chat

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startPlacedIn(t *testing.T, pgid int, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	return cmd
}

func placedExited(cmd *exec.Cmd) (done chan struct{}) {
	done = make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return
}

func placedDead(d chan struct{}) bool {
	select {
	case <-d:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

// A turn placed in a run's group is not its own group leader: ending it by signal must reach it (and what it started) and
// no other turn of the group.
func TestSignalsReachAPlacedTurnAndNotItsGroupMates(t *testing.T) {
	for name, sig := range map[string]func(int) error{"terminate": terminateGroup, "kill": killGroup} {
		t.Run(name, func(t *testing.T) {
			anchor := startPlacedIn(t, 0, "sleep 300") // leads the run's group
			g := anchor.Process.Pid
			a := startPlacedIn(t, g, "sleep 300 & wait")
			b := startPlacedIn(t, g, "sleep 300 & wait")
			time.Sleep(300 * time.Millisecond) // the shells have started their children
			if pg, _ := syscall.Getpgid(a.Process.Pid); pg != g {
				t.Fatalf("the turn did not join the group: %d != %d", pg, g)
			}
			da, db := placedExited(a), placedExited(b)
			if err := sig(a.Process.Pid); err != nil {
				t.Fatalf("signalling a placed turn: %v", err)
			}
			if !placedDead(da) {
				t.Fatal("the placed turn was not signalled")
			}
			select {
			case <-db:
				t.Fatal("another turn of the group was signalled")
			case <-time.After(500 * time.Millisecond):
			}
			if err := syscall.Kill(anchor.Process.Pid, 0); err != nil {
				t.Fatalf("the group's anchor was signalled: %v", err)
			}
			// its own child went with it: nothing of the group but b (and its child) and the anchor remains
			out, _ := exec.Command("ps", "-o", "pid=", "-g", placedItoa(g)).Output()
			if n := len(placedFields(string(out))); n > 4 { // anchor, b, b's sleep (+ a zombie not yet reaped is not listed)
				t.Fatalf("a's descendants were left running: %q", out)
			}
		})
	}
}

func placedItoa(i int) string        { return strconv.Itoa(i) }
func placedFields(s string) []string { return strings.Fields(s) }
