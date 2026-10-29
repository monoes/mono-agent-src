package chat

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestStatStartTime(t *testing.T) {
	for name, tc := range map[string]struct{ stat, want string }{
		"plain":                    {"42 (sleep) S 1 42 42 0 -1 4194560 100 0 0 0 0 0 0 0 20 0 1 0 987654 1 2", "987654"},
		"comm with ) and spaces":   {"42 (a) b (c) S 1 42 42 0 -1 4194560 100 0 0 0 0 0 0 0 20 0 1 0 555 1 2", "555"},
		"truncated":                {"42 (sleep) S 1 42", ""},
		"no closing parenthesis":   {"42 sleep S 1 42 42 0", ""},
		"empty":                    {"", ""},
		"exactly the last field":   {"42 (x) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777", "777"},
		"one field short of start": {"42 (x) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17", ""},
	} {
		if got := statStartTime(tc.stat); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// Only ESRCH means a pid is gone: a process of another user answers EPERM and exists.
func TestProcessAliveCountsAProcessItCannotSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no signals")
	}
	if !processAlive(1) {
		t.Error("init is reported as gone")
	}
	if processAlive(1 << 30) {
		t.Error("a pid that cannot exist is reported alive")
	}
}

func runnerWithChildren(t *testing.T, script string) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the runner's children come from /proc")
	}
	c := exec.Command("sh", "-c", script)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { c.Wait(); close(reaped) }()
	t.Cleanup(func() { c.Process.Kill(); <-reaped })
	return c.Process.Pid
}

// The turn's marker is read from the runner's children, never taken from anywhere else.
func TestLearnTurnMarker(t *testing.T) {
	old := markerPoll
	markerPoll = 3 * time.Second
	t.Cleanup(func() { markerPoll = old })

	one := runnerWithChildren(t, execTreeEnv+"=tok-one sleep 30 & wait")
	if got := learnTurnMarker(context.Background(), one, testLauncher(t, one)); got != "tree:tok-one" {
		t.Errorf("one marked child: %q", got)
	}
	two := runnerWithChildren(t, execTreeEnv+"=tok-a sleep 30 & "+execTreeEnv+"=tok-b sleep 30 & wait")
	for i := 0; i < 100 && len(childrenOf(two)) < 2; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if got := learnTurnMarker(context.Background(), two, testLauncher(t, two)); got != "" {
		t.Errorf("children with different markers must not be guessed between: %q", got)
	}
	none := runnerWithChildren(t, "sleep 30 & wait")
	markerPoll = 300 * time.Millisecond
	if got := learnTurnMarker(context.Background(), none, testLauncher(t, none)); got != "" {
		t.Errorf("a child with no marker: %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := learnTurnMarker(ctx, none, testLauncher(t, none)); got != "" {
		t.Errorf("a cancelled lookup: %q", got)
	}
}

// The runner the lookup reads must be one this process started: a pid that is not below it (the
// event names init, or someone else's process) is not read at all.
func TestLearnTurnMarkerIgnoresARunnerThatIsNotOurs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc")
	}
	old := markerPoll
	markerPoll = 300 * time.Millisecond
	t.Cleanup(func() { markerPoll = old })
	if got := learnTurnMarker(context.Background(), 1, testLauncher(t, os.Getpid())); got != "" {
		t.Errorf("init as the runner: %q", got)
	}
	if got := learnTurnMarker(context.Background(), 0, testLauncher(t, os.Getpid())); got != "" {
		t.Errorf("pid 0: %q", got)
	}
}

func TestStartComparable(t *testing.T) {
	for name, tc := range map[string]struct {
		a, b string
		want bool
	}{
		"same form":           {"aabbccddeeff:123", "112233445566:9", true},
		"raw boot id vs hash": {"3f2c1e0a-9b8d-4c7e-a1b2-c3d4e5f60718:123", "112233445566:9", false},
		"no colon":            {"123", "112233445566:9", false},
	} {
		if got := startComparable(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// The runner may be started through a launcher: a process two levels below this one is ours; one that
// is not below it is not.
func TestDescendantOf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc")
	}
	outer := runnerWithChildren(t, `sh -c 'sleep 30 & wait' & wait`)
	var inner int
	for i := 0; i < 100 && inner == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		for _, k := range childrenOf(outer) {
			if len(childrenOf(k)) > 0 {
				inner = childrenOf(k)[0]
			}
		}
	}
	if inner == 0 {
		t.Fatal("no process two levels down")
	}
	if !descendantOf(inner, os.Getpid()) {
		t.Error("a process two levels below this one is not recognised")
	}
	if descendantOf(1, os.Getpid()) || descendantOf(inner, 1<<30) {
		t.Error("a process that is not below the ancestor is recognised")
	}
}

// The runner must be the launcher's process or below it: a runner of the same host that the launcher
// did not start is not read, and neither is a launcher of 0.
func TestLearnTurnMarkerNeedsTheRunnerBelowTheLauncher(t *testing.T) {
	old := markerPoll
	markerPoll = 300 * time.Millisecond
	t.Cleanup(func() { markerPoll = old })
	a := runnerWithChildren(t, execTreeEnv+"=tok-a sleep 30 & wait")
	b := runnerWithChildren(t, execTreeEnv+"=tok-b sleep 30 & wait")
	time.Sleep(200 * time.Millisecond)
	if got := learnTurnMarker(context.Background(), a, testLauncher(t, b)); got != "" {
		t.Errorf("a runner that is not below the launcher: %q", got)
	}
	if got := learnTurnMarker(context.Background(), a, nil); got != "" {
		t.Errorf("no launcher: %q", got)
	}
	markerPoll = 3 * time.Second
	if got := learnTurnMarker(context.Background(), a, testLauncher(t, a)); got != "tree:tok-a" {
		t.Errorf("the launcher's own process: %q", got)
	}
}

// testLauncher pins pid as the launcher, for the lookup tests.
func testLauncher(t *testing.T, pid int) *pinnedLauncher {
	t.Helper()
	l := pinLauncher(pid)
	if l == nil {
		t.Skipf("pid %d cannot be pinned here (no pidfd, or not this process's child)", pid)
	}
	t.Cleanup(l.close)
	return l
}

// A launcher that has exited is no longer the process that was pinned, whatever a delayed or forged
// start event names, and whoever has the pid since: nothing is learned.
func TestLearnTurnMarkerRefusesAnExitedLauncher(t *testing.T) {
	old := markerPoll
	markerPoll = 300 * time.Millisecond
	t.Cleanup(func() { markerPoll = old })
	c := exec.Command("sh", "-c", execTreeEnv+"=tok-gone sleep 30 >/dev/null 2>&1 & wait")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	for i := 0; i < 100 && len(childrenOf(pid)) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	l := testLauncher(t, pid)
	if got := learnTurnMarker(context.Background(), pid, l); got != "tree:tok-gone" {
		t.Fatalf("the live launcher: %q", got)
	}
	c.Process.Kill()
	c.Wait() // reaped: the pid is free to be reused
	if l.current() {
		t.Error("the pin still takes an exited launcher for the process it pinned")
	}
	if got := learnTurnMarker(context.Background(), pid, l); got != "" {
		t.Errorf("an exited launcher: %q", got)
	}
}

// The lookup consults the launcher's pin: with the recorded start time not the process's own (what a
// reused pid looks like) nothing is learned even though the children are there.
func TestLearnTurnMarkerConsultsTheLauncherPin(t *testing.T) {
	old := markerPoll
	markerPoll = 3 * time.Second
	t.Cleanup(func() { markerPoll = old })
	a := runnerWithChildren(t, execTreeEnv+"=tok-pin sleep 30 >/dev/null 2>&1 & wait")
	for i := 0; i < 100 && len(childrenOf(a)) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	l := testLauncher(t, a)
	l.start = "elsewhere:1"
	if got := learnTurnMarker(context.Background(), a, l); got != "" {
		t.Errorf("a launcher whose start time is not the pinned one: %q", got)
	}
}

// A pid that is not this process's child is not taken for the launcher it was reported as.
func TestPinLauncherOnlyPinsAChildOfThisProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc")
	}
	if l := pinLauncher(1); l != nil {
		t.Error("init was pinned as a launcher")
	}
	// A grandchild is not this process's child.
	outer := runnerWithChildren(t, "sleep 30 & wait")
	for i := 0; i < 100 && len(childrenOf(outer)) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if kids := childrenOf(outer); len(kids) > 0 {
		if l := pinLauncher(kids[0]); l != nil {
			l.close()
			t.Error("a grandchild was pinned as a launcher")
		}
	}
}

// The pin is released once, however often it is closed.
func TestPinnedLauncherCloseIsIdempotent(t *testing.T) {
	a := runnerWithChildren(t, "sleep 30 & wait")
	l := testLauncher(t, a)
	l.close()
	l.close()
	var none *pinnedLauncher
	none.close()
}
