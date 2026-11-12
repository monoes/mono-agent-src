//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recordGroupKill makes groupKill record what it sends (and still send it).
func recordGroupKill(t *testing.T) func() []syscall.Signal {
	t.Helper()
	var mu sync.Mutex
	var sent []syscall.Signal
	prev := groupKill
	groupKill = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		sent = append(sent, sig)
		mu.Unlock()
		return syscall.Kill(pid, sig)
	}
	t.Cleanup(func() { groupKill = prev })
	return func() []syscall.Signal {
		mu.Lock()
		defer mu.Unlock()
		return append([]syscall.Signal(nil), sent...)
	}
}

// #235: shutdown must stop a monoagentcli group with SIGTERM, which it turns
// into a cancel that stops its monomind child, and wait for it — not SIGKILL
// it, which orphans that child.
func TestShutdownStopsGroupsGracefully(t *testing.T) {
	withGrace(t, 5*time.Second)
	sent := recordGroupKill(t)
	cmd, done := startGroup(t, context.Background(), "sleep 30")
	plain := exec.Command("sleep", "30") // a workflow/node run: not a group
	if err := plain.Start(); err != nil {
		t.Fatal(err)
	}
	plainDone := make(chan error, 1)
	go func() { plainDone <- plain.Wait() }()

	a := &App{runningCmds: map[string]*exec.Cmd{"orgevents:acme": cmd, "noderun:1": plain}}
	start := time.Now()
	a.stopRunningCmds()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("stopRunningCmds took %s for a group that exits on SIGTERM", d)
	}
	select {
	case err := <-done:
		if sig := exitSignal(err); sig != syscall.SIGTERM {
			t.Errorf("group exit = %v (signal %v), want SIGTERM", err, sig)
		}
	case <-time.After(5 * time.Second): // reaped closes just before done is sent
		t.Fatal("stopRunningCmds returned before the group was reaped")
	}
	if got := sent(); len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Errorf("group signals = %v, want just SIGTERM", got)
	}
	select {
	case err := <-plainDone:
		if sig := exitSignal(err); sig != syscall.SIGKILL {
			t.Errorf("plain exit = %v (signal %v), want SIGKILL", err, sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the non-group command was not killed")
	}
}

// A group that ignores SIGTERM is SIGKILLed after the grace period, and
// shutdown waits for that rather than exiting first.
func TestShutdownWaitsForGraceThenKills(t *testing.T) {
	withGrace(t, 500*time.Millisecond)
	sent := recordGroupKill(t)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd, done := startGroup(t, context.Background(), fmt.Sprintf(`trap "" TERM; : > %q; sleep 30 & wait; wait`, ready))
	// Wait for sh to install its trap: a SIGTERM before that ends it early.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sh never installed its TERM trap")
		}
	}

	a := &App{runningCmds: map[string]*exec.Cmd{"orgrun:acme": cmd}}
	start := time.Now()
	a.stopRunningCmds()
	if d := time.Since(start); d < 500*time.Millisecond {
		t.Errorf("stopRunningCmds returned after %s, before the grace period", d)
	}
	select {
	case err := <-done:
		if sig := exitSignal(err); sig != syscall.SIGKILL {
			t.Errorf("exit = %v (signal %v), want SIGKILL", err, sig)
		}
	case <-time.After(5 * time.Second): // reaped closes just before done is sent
		t.Fatal("stopRunningCmds returned before the group was reaped")
	}
	if got := sent(); len(got) != 2 || got[0] != syscall.SIGTERM || got[1] != syscall.SIGKILL {
		t.Errorf("signals = %v, want SIGTERM then SIGKILL", got)
	}
}

// #235: a stop that runs before its stream is registered must still end
// that stream, as soon as it starts.
func TestStopOrgEventsBeforeStreamRegisters(t *testing.T) {
	withGrace(t, 5*time.Second)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "cli.pid")
	cli := filepath.Join(dir, fakeCLIName)
	script := fmt.Sprintf("#!/bin/sh\necho $$ > %q.tmp && mv %q.tmp %q\nexec sleep 30\n", pidFile, pidFile, pidFile)
	if err := writeFakeCLI(cli, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", cli)
	a := newTestApp(t)

	if got := a.StopOrgEvents("acme", "s1"); !strings.Contains(got, `"ok":false`) {
		t.Fatalf("StopOrgEvents with nothing running = %s", got)
	}
	if got := a.StreamOrgEvents("acme", "s1"); got != `{"ok":true,"stopped":true}` {
		t.Fatalf("StreamOrgEvents after its stop = %s, want it stopped", got)
	}
	a.runningMu.Lock()
	_, registered := a.runningCmds["orgevents:acme"]
	a.runningMu.Unlock()
	if registered {
		t.Fatal("a stopped stream was registered")
	}

	// The stream's CLI was started, then killed at once (possibly before it
	// wrote its pid) and reaped: nothing is left tracked as unreaped.
	deadline := time.Now().Add(5 * time.Second)
	for trackedChatGroups() > 0 {
		if time.Now().After(deadline) {
			if b, err := os.ReadFile(pidFile); err == nil {
				if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
			t.Fatal("the stream stopped before it registered is still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func trackedChatGroups() int {
	n := 0
	chatGroupReaped.Range(func(any, any) bool { n++; return true })
	return n
}

// A stop names its stream, so a late stop for a superseded stream leaves
// the newer one running.
func TestStopOrgEventsLeavesNewerStream(t *testing.T) {
	withGrace(t, 5*time.Second)
	first, firstDone := startGroup(t, context.Background(), "sleep 30")
	second, secondDone := startGroup(t, context.Background(), "sleep 30")
	a := &App{runningCmds: map[string]*exec.Cmd{}}
	key := "orgevents:acme"
	if !a.registerOrgEvents(key, "s1", first) || !a.registerOrgEvents(key, "s2", second) {
		t.Fatal("registerOrgEvents refused a stream nobody stopped")
	}
	select {
	case <-firstDone: // superseded
	case <-time.After(3 * time.Second):
		t.Fatal("the superseded stream was not stopped")
	}
	a.unregisterOrgEvents(key, first) // its reader ending must not drop the newer stream

	if got := a.StopOrgEvents("acme", "s1"); !strings.Contains(got, `"ok":false`) {
		t.Fatalf("stale StopOrgEvents = %s", got)
	}
	select {
	case err := <-secondDone:
		t.Fatalf("a stale stop ended the newer stream: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if got := a.StopOrgEvents("acme", "s2"); got != `{"ok":true}` {
		t.Fatalf("StopOrgEvents = %s", got)
	}
	select {
	case <-secondDone:
	case <-time.After(3 * time.Second):
		t.Fatal("StopOrgEvents did not stop its stream")
	}
}
