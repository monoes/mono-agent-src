//go:build !windows

package chat

import (
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// An adopted turn's group is ended even when its leader exits on SIGTERM first: a child in the
// group that ignores SIGTERM is pinned while the leader still is the turn's, and killed through the
// pin after the grace.
func TestAdoptedTurnStopKillsAChildThatIgnoredTerm(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the group's members come from /proc")
	}
	leader := exec.Command("sh", "-c", `trap 'exit 0' TERM; (trap '' TERM; exec sleep 60) & wait`)
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { leader.Wait(); close(reaped) }()
	t.Cleanup(func() { syscall.Kill(-leader.Process.Pid, syscall.SIGKILL); <-reaped })
	var kid int
	for i := 0; i < 100 && kid == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		if kids := childrenOf(leader.Process.Pid); len(kids) > 0 {
			kid = kids[0]
		}
	}
	if kid == 0 {
		t.Fatal("the leader has no child")
	}
	// Give the shell time to install its trap, then adopt it the way a restarted host would.
	time.Sleep(200 * time.Millisecond)
	stamp := stampOf(leader.Process.Pid)
	if stamp == "" {
		t.Skip("no process start times here")
	}
	turn := &adoptedTurn{pid: leader.Process.Pid, stamp: stamp}
	oldGrace := hostTurnGrace
	hostTurnGrace = 300 * time.Millisecond
	t.Cleanup(func() { hostTurnGrace = oldGrace })
	t.Cleanup(turn.bg.Wait)

	turn.Stop()
	waitUntil(t, "the child that ignored SIGTERM to be killed", func() bool { return !processAliveNotZombie(kid) })
}

// processAliveNotZombie says the process runs: a zombie waiting to be reaped has ended.
func processAliveNotZombie(pid int) bool { return processAlive(pid) && procStateLetter(pid) != 'Z' }
