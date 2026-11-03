//go:build linux

package chat

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pidOf(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 300; i++ {
		if b, err := os.ReadFile(path); err == nil {
			if p, _ := strconv.Atoi(strings.TrimSpace(string(b))); p > 1 {
				return p
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

func dead(pid int) bool {
	for i := 0; i < 300; i++ {
		if syscall.Kill(pid, 0) != nil || processState(pid) == "Z" {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// A refused release of a bound turn ends it before it was given anything: the start fails, the process is dead, and the credential frame
// was never delivered (the child reads it from its pipe, so it never ran past that read).
func TestARefusedReleaseOfABoundTurnStopsItBeforeAnythingIsDelivered(t *testing.T) {
	r := newBoundRig(t, "")
	var gotBound bool
	r.l.beforeRelease = func(_, _, _ string, bound bool) error { gotBound = bound; return errors.New("no ticket") }
	if _, err := r.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "no ticket") {
		t.Fatalf("a refused release: %v", err)
	}
	if !gotBound {
		t.Error("the hook was not told the turn is bound")
	}
	if pid := pidOf(t, r.pidfile); pid > 1 && !dead(pid) {
		t.Error("the refused turn's process is still running")
	}
	if b, _ := os.ReadFile(r.frame); len(b) != 0 {
		t.Errorf("a refused turn was given its credentials (%d bytes)", len(b))
	}
}

// An accepted release changes nothing; no hook is the launch as it was.
func TestAnAcceptedReleaseAndNoHookLaunchAsBefore(t *testing.T) {
	for _, withHook := range []bool{true, false} {
		r := newBoundRig(t, "")
		called := 0
		if withHook {
			r.l.beforeRelease = func(_, _, _ string, bound bool) error { called++; return nil }
		}
		turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
		if err != nil {
			t.Fatalf("hook=%v: %v", withHook, err)
		}
		_ = os.WriteFile(r.release, []byte("x"), 0o600)
		_ = turn.Wait()
		if withHook && called != 1 {
			t.Errorf("the hook was called %d times", called)
		}
	}
}

// The run is stamped on the session after the identity and before the waiter starts; a failed stamp stops the process like a failed identity.
func TestTheRunIsStampedAndAFailedStampStopsTheProcess(t *testing.T) {
	r := newBoundRig(t, "")
	var order []string
	r.l.onProcess = func(_, _, _ string, _ int, _ string, _ bool) error { order = append(order, "identity"); return nil }
	r.l.runStamp = func(p, c string) (string, int64) { return "charter-1", 7 }
	r.l.recordRun = func(p, c, turn, charter string, gen int64) error {
		order = append(order, "run:"+charter+":"+strconv.FormatInt(gen, 10)+":"+turn)
		return nil
	}
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if strings.Join(order, ",") != "identity,run:charter-1:7:t1" {
		t.Errorf("order %v", order)
	}
	r2 := newBoundRig(t, "")
	r2.l.runStamp = func(string, string) (string, int64) { return "charter-1", 7 }
	r2.l.recordRun = func(string, string, string, string, int64) error { return errors.New("db down") }
	if _, err := r2.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("a failed stamp: %v", err)
	}
	if pid := pidOf(t, r2.pidfile); pid > 1 && !dead(pid) {
		t.Error("the process of a turn whose run could not be stamped is still running")
	}
	r3 := newBoundRig(t, "") // a zero answer stamps nothing
	r3.l.runStamp = func(string, string) (string, int64) { return "", 0 }
	r3.l.recordRun = func(string, string, string, string, int64) error { t.Error("a zero answer was stamped"); return nil }
	turn3, err := r3.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(r3.release, []byte("x"), 0o600)
	_ = turn3.Wait()
}
