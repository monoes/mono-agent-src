//go:build !windows

package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// The stand-in daemon is this test binary run again (see TestLockAcrossProcesses in
// internal/daemonhb for the same idiom): stopDaemonHelperEnv names what it plays, and
// TestStopDaemonHelperProcess is the entry point the parent runs it through. Unix-only:
// terminateProcess and killProcess are stubs on Windows (coder_signal_windows.go), so
// stopDaemon cannot reach this path there.
//
//	"lock"     the daemon: takes the single-instance lock, writes its heartbeat, and on SIGTERM
//	           removes the heartbeat (the real daemon's heartbeat goroutine does that as soon as
//	           its context ends) and exits;
//	"nolock"   a daemon built before the lock existed: a heartbeat and no lock, and a shutdown
//	           that takes a moment;
//	"stubborn" the daemon again, but it ignores SIGTERM, so only SIGKILL stops it.
const stopDaemonHelperEnv = "MONOAGENTCLI_STOPDAEMON_HELPER"

// runStopDaemonHelper plays mode. It asks for SIGTERM before it takes the lock: a signal sent
// the moment the lock becomes visible must find a handler.
func runStopDaemonHelper(mode string) {
	// Never outlive the test run that started it: a `go test -timeout` panic or a killed parent
	// would leave a daemon that ignores SIGTERM (mode "stubborn") running for good.
	time.AfterFunc(time.Minute, func() { os.Exit(3) })
	// TestMain gave this process a temporary home that its own cleanup removes at the end, which
	// SIGKILL and the timeout above skip: remove it now. Only what testhome made, by its name.
	if home := os.Getenv("HOME"); strings.HasPrefix(filepath.Base(home), "monoagent-testhome-") {
		_ = os.RemoveAll(home)
	}
	sig := make(chan os.Signal, 1)
	if mode == "stubborn" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(sig, syscall.SIGTERM)
	}
	if mode != "nolock" {
		release, err := daemonhb.Lock()
		if err != nil {
			os.Exit(1)
		}
		defer release()
	}
	if err := daemonhb.Write(daemonhb.Heartbeat{PID: os.Getpid()}); err != nil {
		os.Exit(1)
	}
	if mode == "stubborn" {
		select {} // only SIGKILL ends it
	}
	<-sig
	_ = os.Remove(daemonhb.Path())
	if mode == "nolock" {
		time.Sleep(300 * time.Millisecond)
	}
}

// TestStopDaemonHelperProcess is not a test: it is the entry point of the stand-in daemon.
// It returns (and does not os.Exit) so that TestMain removes the temporary home it made.
func TestStopDaemonHelperProcess(t *testing.T) {
	mode := os.Getenv(stopDaemonHelperEnv)
	if mode == "" {
		t.Skip("entry point of the stand-in daemon, run by the stopDaemon tests")
	}
	runStopDaemonHelper(mode)
}

// startStopDaemonHelper starts the stand-in daemon in mode and returns once this machine's
// heartbeat names it, which it writes only after it holds the lock (the parent never probes the
// lock meanwhile: a probe takes it for an instant and could make the helper's own attempt
// fail). It reaps the helper in the background, as a service manager would: a child nobody
// waits for stays a zombie that `kill -0` still finds.
func startStopDaemonHelper(t *testing.T, mode string) (pid int, exited <-chan error) {
	t.Helper()
	pid, exited, _ = startStopDaemonHelperIn(t, mode)
	return pid, exited
}

// startStopDaemonHelperIn is startStopDaemonHelper that also returns the private temporary
// directory the helper was given, where it makes its own temporary home.
func startStopDaemonHelperIn(t *testing.T, mode string) (pid int, exited <-chan error, tmp string) {
	t.Helper()
	cmd, tmp := startHelper(t, mode)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd.Process.Pid, done, tmp
}

// startUnreapedStopDaemonHelper is the same helper with nobody waiting for it: once it has
// exited it stays a zombie until the test ends, as a daemon this process started is to this
// process until it waits for it. Such a process still answers `kill -0`, and its lock is free.
func startUnreapedStopDaemonHelper(t *testing.T, mode string) (pid int) {
	t.Helper()
	cmd, _ := startHelper(t, mode)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func startHelper(t *testing.T, mode string) (cmd *exec.Cmd, tmp string) {
	t.Helper()
	hbPath := filepath.Join(t.TempDir(), "hb.json")
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", hbPath)
	tmp = t.TempDir()

	cmd = exec.Command(os.Args[0], "-test.run=^TestStopDaemonHelperProcess$")
	// atexit_sleep_ms=0: the race runtime otherwise sleeps a second before the process exits, and
	// the old pid would outlive its lock by that second, which no daemon does.
	race := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
	cmd.Env = append(os.Environ(), stopDaemonHelperEnv+"="+mode, "MONOAGENT_DAEMON_HEARTBEAT="+hbPath,
		"TMPDIR="+tmp, "GORACE="+race)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() }) // in case the test fails before stopDaemon stops it

	deadline := time.Now().Add(45 * time.Second) // under the helper's own minute
	for {
		if hb, ok := daemonhb.Read(); ok && hb.PID == cmd.Process.Pid {
			return cmd, tmp
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s helper never became the daemon", mode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// shortGrace shortens how long stopDaemon waits for a SIGTERM'd daemon for the rest of the test.
func shortGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old, oldKill := daemonStopGrace, daemonKillWait
	daemonStopGrace, daemonKillWait = d, 30*time.Second
	t.Cleanup(func() { daemonStopGrace, daemonKillWait = old, oldKill })
}

// A stand-in daemon takes about 0.1 s to exit after SIGTERM, and about 1.2 s under the race
// detector, which CI uses. The tests that must not wait out the grace give it much more than
// that and then check that stopDaemon came back well before it ran out: a regression that waits
// for the wrong thing shows as an error or as a wait of the whole grace, whatever the machine.
const (
	roomyGrace = 10 * time.Second
	wellBefore = 5 * time.Second
)

func TestStopDaemonSignalsAndWaitsForTheLockToClear(t *testing.T) {
	pid, exited := startStopDaemonHelper(t, "lock")

	var progressLines []string
	err := stopDaemon(context.Background(), pid, func(line string) { progressLines = append(progressLines, line) })
	if err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	if daemonhb.Locked() {
		t.Fatal("lock still held after stopDaemon returned")
	}
	if len(progressLines) == 0 {
		t.Error("expected at least one progress line")
	}

	waitErr := <-exited
	var exitErr *exec.ExitError
	if waitErr != nil && (!errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 0) {
		t.Errorf("helper process exit: %v", waitErr)
	}
}

// A daemon that this process started and has not waited for stays a zombie once it has exited:
// it still answers kill -0, and what tells that it has stopped is that its lock is free. A wait
// for the process to disappear would run out the grace, and a SIGKILL cannot end a zombie.
func TestStopDaemonDoesNotWaitForAZombieToDisappear(t *testing.T) {
	pid := startUnreapedStopDaemonHelper(t, "lock")
	shortGrace(t, roomyGrace)

	start := time.Now()
	if err := stopDaemon(context.Background(), pid, func(string) {}); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	if daemonhb.Locked() {
		t.Error("the lock is still held")
	}
	if waited := time.Since(start); waited > wellBefore {
		t.Errorf("stopDaemon took %v: it waited for the zombie to disappear", waited)
	}
}

func TestStopDaemonNoopWhenAlreadyStopped(t *testing.T) {
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
	called := false
	err := stopDaemon(context.Background(), 999999, func(string) { called = true })
	if err != nil {
		t.Fatalf("stopDaemon on an already-clear lock: %v", err)
	}
	if called {
		t.Error("no progress expected when there was nothing to stop")
	}
}

// A daemon built before the single-instance lock holds none: a lock that reads free says
// nothing about whether it runs, so it is stopped like any other and its exit is waited for.
func TestStopDaemonStopsADaemonThatHoldsNoLock(t *testing.T) {
	pid, _ := startStopDaemonHelper(t, "nolock")

	if err := stopDaemon(context.Background(), pid, func(string) {}); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	if daemonhb.ProcessAlive(pid) {
		t.Fatal("stopDaemon returned while the daemon was still running")
	}
}

// A service manager that keeps the daemon alive (launchd's KeepAlive) starts a new one the
// instant the old one exits, and that one holds the lock before a poll could see it free:
// what stopDaemon waits for is the old process, and it must not mistake the new daemon's lock
// for the old daemon not having stopped.
func TestStopDaemonDoesNotWaitForTheLockOfARespawnedDaemon(t *testing.T) {
	pid, _ := startStopDaemonHelper(t, "lock")
	shortGrace(t, roomyGrace)

	stop := make(chan struct{})
	respawned := make(chan func(), 1)
	go func() { // the service manager: takes the lock the moment the old daemon lets it go
		for {
			select {
			case <-stop:
				return
			default:
			}
			if release, err := daemonhb.Lock(); err == nil {
				respawned <- release
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		select {
		case release := <-respawned:
			release()
		default:
		}
	})

	start := time.Now()
	if err := stopDaemon(context.Background(), pid, func(string) {}); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	if waited := time.Since(start); waited > wellBefore {
		t.Errorf("stopDaemon took %v: it waited for the lock of the new daemon", waited)
	}
}

func TestStopDaemonForcesADaemonThatIgnoresSIGTERM(t *testing.T) {
	pid, exited := startStopDaemonHelper(t, "stubborn")
	// The helper ignores SIGTERM before its heartbeat names it, so a grace this short is
	// deterministic: SIGKILL is the only thing that can stop it, however slow the machine.
	shortGrace(t, time.Millisecond)

	var lines []string
	if err := stopDaemon(context.Background(), pid, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	// stopDaemon returns once the lock is free, which is the moment the process dies and
	// before the reaper goroutine has waited for it: until then it is a zombie, which
	// `kill -0` still finds. Wait for the reaper instead of racing it.
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon is still running after SIGKILL")
	}
	if daemonhb.ProcessAlive(pid) {
		t.Fatal("the daemon is still running after SIGKILL")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "forcing") {
		t.Errorf("the forced stop was not announced: %q", lines)
	}
}

// A helper that is killed skips TestMain's cleanup, so it removes its temporary home itself,
// early: a run of these tests must not leave a monoagent-testhome-* directory behind per helper.
func TestStopDaemonHelpersLeaveNoTemporaryHomesBehind(t *testing.T) {
	pid, _, tmp := startStopDaemonHelperIn(t, "stubborn")
	shortGrace(t, 300*time.Millisecond)

	if err := stopDaemon(context.Background(), pid, func(string) {}); err != nil { // ends it with SIGKILL
		t.Fatalf("stopDaemon: %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "monoagent-testhome-") {
			t.Errorf("a killed helper left its temporary home %s behind", e.Name())
		}
	}
}

func TestStopDaemonStopsWaitingWhenTheContextEnds(t *testing.T) {
	pid, _ := startStopDaemonHelper(t, "stubborn")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := stopDaemon(ctx, pid, func(string) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopDaemon = %v, want the context's error", err)
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("stopDaemon kept waiting for %v after its context ended", waited)
	}
}

// Only the daemon the heartbeat names is signaled: the pid of any other live process, this
// test's own included, is refused and nothing is sent to it.
func TestStopDaemonRefusesAPidThatIsNotTheDaemon(t *testing.T) {
	pid, _ := startStopDaemonHelper(t, "lock")

	if err := stopDaemon(context.Background(), os.Getpid(), func(string) {}); err == nil {
		t.Fatal("stopDaemon accepted the pid of a process that is not the daemon")
	}
	if !daemonhb.ProcessAlive(pid) {
		t.Fatal("the real daemon was stopped by a call for another pid")
	}
}

// kill(0), kill(-1) and kill(1) are a process group, every process and init: never sent.
func TestStopDaemonRefusesPidsThatAreNeverADaemon(t *testing.T) {
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
	for _, pid := range []int{-1, 0, 1} {
		if err := stopDaemon(context.Background(), pid, func(string) {}); err == nil {
			t.Errorf("stopDaemon(%d) did not refuse", pid)
		}
	}
}
