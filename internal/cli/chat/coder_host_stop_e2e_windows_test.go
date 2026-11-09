//go:build windows

package chat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// The real stop on Windows, with real processes: this test binary re-executed as a stand-in host that holds the host
// lock (LockFileEx) and, if it is the cooperative one, the stop event, as `host run` does.
//   coop: the event stops it; it leaves through its cleanup (a marker file), nothing is terminated, the lock is free.
//   deaf: it has the event and ignores it; after the grace period the pinned handle is terminated, no cleanup runs, the lock is free.
//   noevent: no event at all (an older host): ended at once, and the reason says so.
//   recycled: a wrong start time is refused and the process is untouched.

const (
	winStopRole     = "HOSTSTOP_WINE2E_ROLE"
	winStopDir      = "HOSTSTOP_WINE2E_DIR"
	winStopInstance = "HOSTSTOP_WINE2E_INSTANCE"
)

func init() {
	if role := os.Getenv(winStopRole); role != "" {
		winStopHelper(role)
		os.Exit(0)
	}
}

func winStopHelper(role string) {
	dir := os.Getenv(winStopDir)
	release, err := daemonhb.LockFile(filepath.Join(dir, "host.lock"))
	if err != nil {
		_ = os.WriteFile(filepath.Join(dir, "lockerr"), []byte(err.Error()), 0o600)
		return
	}
	defer release()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if role == "coop" || role == "deaf" { // deaf: the event exists and is set, and nothing is done about it
		onStop := stop
		if role == "deaf" {
			onStop = func() {}
		}
		end, err := watchHostStop(ctx, os.Getenv(winStopInstance), onStop)
		if err != nil {
			_ = os.WriteFile(filepath.Join(dir, "eventerr"), []byte(err.Error()), 0o600)
			return
		}
		defer end()
	}
	_ = os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600)
	select {
	case <-ctx.Done():
		_ = os.WriteFile(filepath.Join(dir, "cleanup"), nil, 0o600) // the deferred cleanup of a real host
	case <-time.After(5 * time.Minute):
	}
}

func startStopHost(t *testing.T, role string) (cmd *exec.Cmd, dir, instance string) {
	t.Helper()
	dir = t.TempDir()
	instance = "host-" + uuid.NewString()[:8]
	self, err := os.Executable() // os.Args[0] may be relative, which exec refuses
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(self, "-test.run=NONE")
	cmd.Env = append(os.Environ(), winStopRole+"="+role, winStopDir+"="+dir, winStopInstance+"="+instance)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	b1, _ := os.ReadFile(filepath.Join(dir, "lockerr"))
	b2, _ := os.ReadFile(filepath.Join(dir, "eventerr"))
	t.Fatalf("the stand-in host never became ready (%s %s)", b1, b2)
	return
}

func lockFree(dir string) bool {
	release, err := daemonhb.LockFile(filepath.Join(dir, "host.lock"))
	if err == nil {
		release()
		return true
	}
	return false
}

func TestWindowsHostStopCooperativeHostLeavesThroughItsCleanup(t *testing.T) {
	cmd, dir, inst := startStopHost(t, "coop")
	info := hostInfo{PID: cmd.Process.Pid, Stamp: pidStamp(cmd.Process.Pid), Instance: inst}
	forced, err := stopPinnedHost(winHostStopOps(20*time.Second), info, "host.json")
	if err != nil || forced != "" {
		t.Fatalf("forced=%q err=%v, want a graceful stop", forced, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cleanup")); err != nil {
		t.Fatal("the host's cleanup did not run")
	}
	if !lockFree(dir) {
		t.Fatal("the host lock is still held")
	}
}

func TestWindowsHostStopDeafHostIsTerminatedAfterTheGrace(t *testing.T) {
	cmd, dir, inst := startStopHost(t, "deaf")
	info := hostInfo{PID: cmd.Process.Pid, Stamp: pidStamp(cmd.Process.Pid), Instance: inst}
	start := time.Now()
	forced, err := stopPinnedHost(winHostStopOps(500*time.Millisecond), info, "host.json")
	if err != nil || !strings.Contains(forced, "grace period") {
		t.Fatalf("forced=%q err=%v, want a forced stop after the grace period", forced, err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("the host was ended before its grace period")
	}
	if _, err := os.Stat(filepath.Join(dir, "cleanup")); err == nil {
		t.Fatal("cleanup ran in a host that was ended by force")
	}
	if !lockFree(dir) {
		t.Fatal("the host lock is still held after the host was ended")
	}
}

// A host with no event (an older one) cannot be asked: it is ended at once, and the reason says so.
func TestWindowsHostStopWithoutAnEventIsForcedAndSaysWhy(t *testing.T) {
	cmd, dir, inst := startStopHost(t, "noevent")
	info := hostInfo{PID: cmd.Process.Pid, Stamp: pidStamp(cmd.Process.Pid), Instance: inst}
	forced, err := stopPinnedHost(winHostStopOps(20*time.Second), info, "host.json")
	if err != nil || !strings.Contains(forced, "graceful stop unavailable") {
		t.Fatalf("forced=%q err=%v", forced, err)
	}
	if !lockFree(dir) {
		t.Fatal("the host lock is still held after the host was ended")
	}
}

func TestWindowsHostStopRefusesARecycledPid(t *testing.T) {
	cmd, dir, inst := startStopHost(t, "coop")
	for _, stamp := range []string{"created:1", ""} {
		info := hostInfo{PID: cmd.Process.Pid, Stamp: stamp, Instance: inst}
		if _, err := stopPinnedHost(winHostStopOps(time.Second), info, "host.json"); err == nil || !strings.Contains(err.Error(), "host.json") {
			t.Fatalf("stamp %q: err %v, want a refusal", stamp, err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if pidStamp(cmd.Process.Pid) == "" || lockFree(dir) {
		t.Fatal("the process with the wrong start time was ended")
	}
	if _, err := os.Stat(filepath.Join(dir, "cleanup")); err == nil {
		t.Fatal("the process with the wrong start time was signalled")
	}
}
