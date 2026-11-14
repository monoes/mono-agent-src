//go:build linux

package chat

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/coderhost"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// capturePid makes the launcher report the pid it recorded for the turn (the process may be stopped before it writes anything itself).
func capturePid(l *execLauncher) *atomic.Int64 {
	var p atomic.Int64
	prev := l.onProcess
	l.onProcess = func(pr, c, tu string, pid int, stamp string, bound bool) error {
		p.Store(int64(pid))
		if prev != nil {
			return prev(pr, c, tu, pid, stamp, bound)
		}
		return nil
	}
	return &p
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
	pidp := capturePid(r.l)
	var gotBound bool
	r.l.beforeRelease = func(_ context.Context, _, _, _ string, bound bool) error {
		gotBound = bound
		return errors.New("no ticket")
	}
	if _, err := r.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "no ticket") {
		t.Fatalf("a refused release: %v", err)
	}
	if !gotBound {
		t.Error("the hook was not told the turn is bound")
	}
	if pid := int(pidp.Load()); pid <= 1 {
		t.Error("the launcher recorded no pid")
	} else if !dead(pid) {
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
			r.l.beforeRelease = func(_ context.Context, _, _, _ string, bound bool) error { called++; return nil }
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
	pid2 := capturePid(r2.l)
	r2.l.runStamp = func(string, string) (string, int64) { return "charter-1", 7 }
	r2.l.recordRun = func(string, string, string, string, int64) error { return errors.New("db down") }
	if _, err := r2.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("a failed stamp: %v", err)
	}
	if pid := int(pid2.Load()); pid <= 1 {
		t.Error("the launcher recorded no pid")
	} else if !dead(pid) {
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

// The release check runs BEFORE anything is delivered: when it runs the child has been given nothing (its frame is still empty, and stays
// empty for as long as we look), and an accepted release lets the frame arrive afterwards.
func TestTheReleaseCheckRunsBeforeTheCredentialsAreDelivered(t *testing.T) {
	r := newBoundRig(t, "")
	r.l.beforeRelease = func(_ context.Context, _, _, _ string, _ bool) error {
		for i := 0; i < 30; i++ { // were delivery already under way, the child would have written its frame by now
			if b, _ := os.ReadFile(r.frame); len(b) != 0 {
				t.Error("the credentials were delivered before the release check ran")
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for i := 0; i < 300 && got == 0; i++ {
		if b, _ := os.ReadFile(r.frame); len(b) != 0 {
			got = len(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if got == 0 {
		t.Error("an accepted release never delivered the credentials")
	}
}

// A legacy (unbound) turn is told bound=false; a refusal stops the already running process.
func TestARefusedReleaseOfALegacyTurnStopsTheStartedProcess(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\necho $$ > \"$(dirname \"$0\")/pid\"\nsleep 300\n")
	var gotBound = true
	var pidp *atomic.Int64
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		revoke: func(string, string) error { return nil },
		beforeRelease: func(_ context.Context, _, _, _ string, bound bool) error {
			gotBound = bound
			return errors.New("unbound is refused")
		}}
	pidp = capturePid(l)
	if _, err := l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "unbound is refused") {
		t.Fatalf("a refused legacy release: %v", err)
	}
	if gotBound {
		t.Error("a legacy turn was reported as bound")
	}
	if pid := int(pidp.Load()); pid <= 1 {
		t.Error("the launcher recorded no pid")
	} else if !dead(pid) {
		t.Error("the refused legacy process is still running")
	}
}

// A panicking hook is a failed start with the process stopped, never an unwound Start with an unwaited child.
func TestAPanickingHookFailsTheStartAndStopsTheProcess(t *testing.T) {
	for name, set := range map[string]func(l *execLauncher){
		"runStamp": func(l *execLauncher) {
			l.runStamp = func(string, string) (string, int64) { panic("boom") }
			l.recordRun = func(string, string, string, string, int64) error { return nil }
		},
		"recordRun": func(l *execLauncher) {
			l.runStamp = func(string, string) (string, int64) { return "c", 1 }
			l.recordRun = func(string, string, string, string, int64) error { panic("boom") }
		},
		"beforeRelease": func(l *execLauncher) {
			l.beforeRelease = func(_ context.Context, _, _, _ string, _ bool) error { panic("boom") }
		},
	} {
		r := newBoundRig(t, "")
		pidp := capturePid(r.l)
		set(r.l)
		if _, err := r.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "panicked") {
			t.Fatalf("%s: a panicking hook: %v", name, err)
		}
		if pid := int(pidp.Load()); pid <= 1 {
			t.Errorf("%s: the launcher recorded no pid", name)
		} else if !dead(pid) {
			t.Errorf("%s: the process is still running", name)
		}
	}
}

// startWithin runs Start and fails the test (cleanly, not by a hung test binary) if it does not return within d.
func startWithin(t *testing.T, d time.Duration, l *execLauncher) error {
	t.Helper()
	res := make(chan error, 1)
	go func() { _, err := l.Start("default", "c1", "t1", "hi", nil, ""); res <- err }()
	select {
	case err := <-res:
		return err
	case <-time.After(d):
		t.Fatal("Start did not return: a hook stalled it")
		return nil
	}
}

// A release check that does not answer refuses at the deadline instead of stalling the start for good, and its late completion takes
// nothing: it sees its context cancelled before it would consume the (simulated) ticket.
func TestABlockingReleaseCheckRefusesAtTheDeadlineAndItsLateEndTakesNothing(t *testing.T) {
	old := releaseTimeout
	releaseTimeout = 100 * time.Millisecond
	t.Cleanup(func() { releaseTimeout = old })
	r := newBoundRig(t, "")
	pidp := capturePid(r.l)
	block := make(chan struct{})
	finished := make(chan struct{})
	var consumed atomic.Int32
	r.l.beforeRelease = func(ctx context.Context, _, _, _ string, _ bool) error {
		defer close(finished)
		<-block
		if ctx.Err() == nil {
			consumed.Add(1) // the ticket
		}
		return nil
	}
	if err := startWithin(t, 15*time.Second, r.l); err == nil || !strings.Contains(err.Error(), "in time") {
		close(block)
		t.Fatalf("a blocking check: %v", err)
	}
	close(block) // the check completes late
	<-finished
	if consumed.Load() != 0 {
		t.Error("a release check that finished after its deadline still consumed the ticket")
	}
	if pid := int(pidp.Load()); pid <= 1 {
		t.Error("the launcher recorded no pid")
	} else if !dead(pid) {
		t.Error("the process of a turn whose release timed out is still running")
	}
}

// A run stamp that does not answer is bounded too: the start fails at the deadline and the unwaited child is stopped.
func TestABlockingRunStampRefusesAtTheDeadlineAndStopsTheProcess(t *testing.T) {
	old := releaseTimeout
	releaseTimeout = 100 * time.Millisecond
	t.Cleanup(func() { releaseTimeout = old })
	for name, set := range map[string]func(l *execLauncher, block chan struct{}){
		"runStamp": func(l *execLauncher, block chan struct{}) {
			l.runStamp = func(string, string) (string, int64) { <-block; return "c", 1 }
			l.recordRun = func(string, string, string, string, int64) error { return nil }
		},
		"recordRun": func(l *execLauncher, block chan struct{}) {
			l.runStamp = func(string, string) (string, int64) { return "c", 1 }
			l.recordRun = func(string, string, string, string, int64) error { <-block; return nil }
		},
	} {
		r := newBoundRig(t, "")
		pidp := capturePid(r.l)
		block := make(chan struct{})
		set(r.l, block)
		err := startWithin(t, 15*time.Second, r.l)
		close(block)
		if err == nil || !strings.Contains(err.Error(), "in time") {
			t.Fatalf("%s: a blocking stamp: %v", name, err)
		}
		if pid := int(pidp.Load()); pid <= 1 {
			t.Errorf("%s: the launcher recorded no pid", name)
		} else if !dead(pid) {
			t.Errorf("%s: the process is still running", name)
		}
	}
}

// The real store writes the run on the real session.
func TestAStartStampsTheRunOnTheRealSession(t *testing.T) {
	r := newBoundRig(t, "")
	x, err := r.store.EnsureSession("default", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = coderhost.StateRunning, "t1"
	if err := r.store.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	r.l.runStamp = func(string, string) (string, int64) { return "charter-1", 7 }
	r.l.recordRun = r.store.StampDoomsdayRun
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	var gen int64
	var charter string
	if err := r.db.DB.QueryRow(`SELECT doomsday_generation, doomsday_charter_id FROM coder_sessions WHERE profile_id = 'default' AND conversation_id = 'c1'`).Scan(&gen, &charter); err != nil || gen != 7 || charter != "charter-1" {
		t.Errorf("the session carries %d %q (%v)", gen, charter, err)
	}
}

// InstallRunHooks sets the hooks on the production launchers, and refuses (as an error, never a panic) what it cannot install into.
func TestInstallRunHooks(t *testing.T) {
	legacy, bound := &execLauncher{}, &execLauncher{}
	h := &coderhost.Host{L: &gatedLauncher{inner: legacy, bound: bound}}
	st := coderhost.NewStore(nil)
	hooks := RunHooks{Place: func(*exec.Cmd, string, string) (func(), error) { return nil, nil }, PlaceRequired: func(string, string) (bool, error) { return false, nil }, RunStamp: func(string, string) (string, int64) { return "c", 1 }, BeforeRelease: func(_ context.Context, _, _, _ string, _ bool) error { return nil }}
	if err := InstallRunHooks(st, h, hooks); err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]*execLauncher{"legacy": legacy, "bound": bound} {
		if l.runStamp == nil || l.recordRun == nil || l.beforeRelease == nil || l.place == nil || l.placeRequired == nil {
			t.Errorf("%s launcher without its hooks: %+v", name, l)
		}
	}
	if err := InstallRunHooks(nil, h, hooks); err == nil {
		t.Error("a nil store was accepted for a stamping hook")
	}
	if err := InstallRunHooks(st, &coderhost.Host{L: &execLauncher{}}, hooks); err == nil {
		t.Error("a host without the production gate was accepted")
	}
	if err := InstallRunHooks(st, nil, hooks); err == nil {
		t.Error("a nil host was accepted")
	}
}

// place runs on cmd AFTER the launcher built its process attributes (a session of its own, a pidfd) and before the process exists; its
// release runs once right after the start; the child is a session leader.
func TestThePlacementRunsAfterTheLauncherBuiltItsAttributesAndIsReleased(t *testing.T) {
	r := newBoundRig(t, "")
	pidp := capturePid(r.l)
	var sawSetsid, sawPidfd bool
	var released atomic.Int32
	var startedBeforeRelease bool
	r.l.place = func(c *exec.Cmd, profile, conv string) (func(), error) {
		sawSetsid = c.SysProcAttr != nil && c.SysProcAttr.Setsid
		sawPidfd = c.SysProcAttr != nil && c.SysProcAttr.PidFD != nil
		if profile != "default" || conv != "c1" {
			t.Errorf("place was given %q %q", profile, conv)
		}
		return func() { startedBeforeRelease = c.Process != nil; released.Add(1) }, nil
	}
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sawSetsid || !sawPidfd {
		t.Errorf("place ran before the launcher's attributes were built (setsid=%v pidfd=%v)", sawSetsid, sawPidfd)
	}
	if released.Load() != 1 || !startedBeforeRelease {
		t.Errorf("release ran %d times, process existed at release: %v", released.Load(), startedBeforeRelease)
	}
	if pid := int(pidp.Load()); pid > 1 {
		if sid, err := unix.Getsid(pid); err != nil || sid != pid {
			t.Errorf("the child is not a session leader (sid %d, %v)", sid, err)
		}
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
}

// A placement that fails (an error or a panic) refuses the turn before anything runs: no process is recorded, its release still runs.
func TestAFailedPlacementRefusesTheTurnBeforeAnythingRuns(t *testing.T) {
	for name, place := range map[string]func(*exec.Cmd, string, string) (func(), error){
		"error": func(*exec.Cmd, string, string) (func(), error) { return nil, errors.New("no cgroup") },
		"panic": func(*exec.Cmd, string, string) (func(), error) { panic("boom") },
	} {
		r := newBoundRig(t, "")
		pidp := capturePid(r.l)
		r.l.place = place
		_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
		if err == nil || (name == "error" && !strings.Contains(err.Error(), "no cgroup")) || (name == "panic" && !strings.Contains(err.Error(), "panicked")) {
			t.Fatalf("%s: %v", name, err)
		}
		if coderhost.TurnStarted(err) {
			t.Errorf("%s: a refused placement is reported as a started turn", name)
		}
		time.Sleep(100 * time.Millisecond)
		if pidp.Load() != 0 {
			t.Errorf("%s: a process was recorded for a turn that was never placed", name)
		}
		if _, statErr := os.Stat(r.pidfile); statErr == nil {
			t.Errorf("%s: the turn's script ran", name)
		}
	}
}

// A release is called even when the placement failed after producing one.
func TestAPlacementReleaseRunsWhenTheStartFails(t *testing.T) {
	r := newBoundRig(t, "")
	var released atomic.Int32
	r.l.place = func(*exec.Cmd, string, string) (func(), error) {
		return func() { released.Add(1) }, errors.New("refused")
	}
	if _, err := r.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil {
		t.Fatal("a refused placement started")
	}
	if released.Load() != 1 {
		t.Errorf("release ran %d times", released.Load())
	}
}

// A placement cannot be honoured by a legacy launch: it is refused rather than started outside it.
func TestALegacyStartWithAPlacementIsRefused(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\necho $$ > \"$(dirname \"$0\")/pid\"\nsleep 300\n")
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		revoke: func(string, string) error { return nil },
		place:  func(*exec.Cmd, string, string) (func(), error) { return nil, nil }}
	if _, err := l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "needs a bound launch") {
		t.Fatalf("a legacy start with a placement: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "pid")); err == nil {
		t.Error("the legacy process ran")
	}
}

// A placement that replaces the launcher's process attributes (dropping the session or the pidfd) is refused BEFORE anything is started.
func TestAPlacementThatReplacesTheProcessAttributesIsRefusedBeforeTheStart(t *testing.T) {
	for name, mutate := range map[string]func(c *exec.Cmd){
		"replaced":   func(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{} },
		"nil":        func(c *exec.Cmd) { c.SysProcAttr = nil },
		"no session": func(c *exec.Cmd) { c.SysProcAttr.Setsid = false },
		"no pidfd":   func(c *exec.Cmd) { c.SysProcAttr.PidFD = nil },
		// an equal value behind another pointer: the launcher would never learn the descriptor, so it is refused
		"another pidfd pointer": func(c *exec.Cmd) { fresh := *c.SysProcAttr.PidFD; c.SysProcAttr.PidFD = &fresh },
	} {
		r := newBoundRig(t, "")
		pidp := capturePid(r.l)
		r.l.place = func(c *exec.Cmd, _, _ string) (func(), error) { mutate(c); return nil, nil }
		_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
		if err == nil || !strings.Contains(err.Error(), "replaced the launcher's process attributes") {
			t.Fatalf("%s: %v", name, err)
		}
		time.Sleep(100 * time.Millisecond)
		if pidp.Load() != 0 {
			t.Errorf("%s: a process was started with attributes the launcher did not build", name)
		}
		if _, statErr := os.Stat(r.pidfile); statErr == nil {
			t.Errorf("%s: the turn's script ran", name)
		}
	}
}

// A release that panics does not unwind the host: the start still succeeds with its child.
func TestAPanickingPlacementReleaseDoesNotUnwindTheStart(t *testing.T) {
	r := newBoundRig(t, "")
	r.l.place = func(*exec.Cmd, string, string) (func(), error) { return func() { panic("release blew up") }, nil }
	var logged atomic.Int32
	r.l.logf = func(string, ...any) { logged.Add(1) }
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatalf("a panicking release failed the start: %v", err)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if logged.Load() == 0 {
		t.Error("the panic of the release was not logged")
	}
}
