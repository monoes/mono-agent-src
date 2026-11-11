//go:build linux || darwin

package chat

import (
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// ----- held leaders: the unit may reap a leader before the launcher saw its exit; nothing then signals the freed number -----

func withKillCounter(t *testing.T, kill func(*os.Process) error) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	old := killLeaderProc
	killLeaderProc = func(p *os.Process) error { n.Add(1); return kill(p) }
	t.Cleanup(func() { killLeaderProc = old })
	return &n
}

// The unit reaped the held leader (its release ran) before the launcher's wait saw the exit: the number is free, so the leader is
// not killed through the handle and no group is signalled by number afterwards (the turn is marked killed, which forbids it).
func TestAHeldLeaderReapedByTheUnitBeforeTheLauncherSawItIsNotSignalledByNumber(t *testing.T) {
	kills := withKillCounter(t, func(p *os.Process) error { return p.Kill() })
	old := holdLeader
	holdLeader = func(pid int) bool { // what the unit's release does: reap it; the launcher's own look finds nothing to hold
		var ws syscall.WaitStatus
		for {
			if _, err := syscall.Wait4(pid, &ws, 0, nil); !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		return false
	}
	t.Cleanup(func() { holdLeader = old })
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(true, nil, nil)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitFile(r.frame)
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	if err := turn.Wait(); err != nil {
		t.Fatalf("the turn's end: %v", err)
	}
	if n := kills.Load(); n != 0 {
		t.Fatalf("a reaped leader was signalled through the handle %d time(s): its number is free", n)
	}
	et := turn.(*execTurn)
	et.mu.Lock()
	killed := et.killed
	et.mu.Unlock()
	if !killed {
		t.Fatal("the turn is not marked killed, so a later stop or grace expiry could signal the freed number")
	}
}

// Guard for the other side: a held leader whose exit cannot be observed and that is still running IS killed (once).
func TestAHeldLeaderStillRunningWhoseExitCannotBeObservedIsKilledOnce(t *testing.T) {
	kills := withKillCounter(t, func(p *os.Process) error { return p.Kill() })
	old := holdLeader
	holdLeader = func(int) bool { return false }
	t.Cleanup(func() { holdLeader = old })
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(true, nil, nil)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil {
		_ = turn.Wait()
	}
	if n := kills.Load(); n != 1 {
		t.Fatalf("the running leader was killed %d times, want once", n)
	}
	if len(h.pids) == 1 && !unreapedZombie(h.pids[0]) {
		t.Errorf("the killed leader is not an unreaped zombie (%q)", processState(h.pids[0]))
	}
	h.reap(t)
}

// A held leader that cannot be confirmed ended within the bound is not marked reaped (it may still be running); the turn still ends,
// with an error that says so.
func TestAHeldLeaderThatCannotBeConfirmedEndedIsNotMarkedReaped(t *testing.T) {
	withKillCounter(t, func(*os.Process) error { return errors.New("not signalled") }) // the kill does nothing (and, failing, signals no group): the leader stays alive
	oldH, oldW := holdLeader, heldGoneWait
	holdLeader = func(int) bool { return false }
	heldGoneWait = 150 * time.Millisecond
	t.Cleanup(func() { holdLeader, heldGoneWait = oldH, oldW })
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(true, nil, nil)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- turn.Wait() }()
	select {
	case werr := <-done:
		if werr == nil || !strings.Contains(werr.Error(), "could not be confirmed") {
			t.Errorf("the turn's end said %v", werr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the turn never ended")
	}
	if turn.(*execTurn).isReaped() {
		t.Error("a leader that may still be running was marked reaped")
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	if len(h.pids) == 1 {
		_ = unreapedZombie(h.pids[0])
	}
	h.reap(t)
}

// ----- a leader that exited before it could be adopted says how -----

func TestAnAdoptionRefusedForALeaderThatAlreadyExitedSaysHowItEnded(t *testing.T) {
	r := newBoundRig(t, "exit 3\n")
	var h holdRig
	time.Sleep(0)
	r.l.adopt = func(p, c string, pid int, stamp string) (bool, error) {
		for i := 0; i < 300 && processState(pid) != "Z"; i++ { // the child has exited by the time the unit looks at it
			time.Sleep(10 * time.Millisecond)
		}
		return h.adopt(false, errors.New("the leader is not running"), nil)(p, c, pid, stamp)
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "the leader is not running") {
		t.Fatalf("the adoption error is lost: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("the process's real exit status is not in the error: %v", err)
	}
}
