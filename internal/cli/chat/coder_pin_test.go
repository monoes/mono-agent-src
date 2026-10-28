package chat

import (
	"context"
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
	if got := learnTurnMarker(context.Background(), one); got != "tree:tok-one" {
		t.Errorf("one marked child: %q", got)
	}
	two := runnerWithChildren(t, execTreeEnv+"=tok-a sleep 30 & "+execTreeEnv+"=tok-b sleep 30 & wait")
	time.Sleep(300 * time.Millisecond)
	if got := learnTurnMarker(context.Background(), two); got != "" {
		t.Errorf("children with different markers must not be guessed between: %q", got)
	}
	none := runnerWithChildren(t, "sleep 30 & wait")
	markerPoll = 300 * time.Millisecond
	if got := learnTurnMarker(context.Background(), none); got != "" {
		t.Errorf("a child with no marker: %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := learnTurnMarker(ctx, none); got != "" {
		t.Errorf("a cancelled lookup: %q", got)
	}
}
