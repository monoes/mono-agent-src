//go:build linux

package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// Every way a start can end with a session token possibly live says so (coderhost.TokensMayBeLive), so the host holds the
// session instead of settling it: one test per path, each with a revoke that always fails.

func failingRevoke(string, string) error { return errors.New("database is locked") }

// endsOwnTokens is a per-turn revoke that works: the tests below make only the launch's own session revoke fail, so that the
// marker they look for has to come from the path under test and not from the turn's exit.
func endsOwnTokens(string, string, string) error { return nil }

func scriptBin(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "fake-monoagentcli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestAStartThatFailsToCreateTheProcessAndCannotEndTheTokenSaysSo(t *testing.T) {
	dir := t.TempDir()
	l := &execLauncher{bin: filepath.Join(dir, "no-such-binary"), dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: failingRevoke}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("a failed process creation whose revoke failed: %v", err)
	}
	if coderhost.TurnStarted(err) {
		t.Error("a failure before any process existed is reported as a started turn")
	}
}

func TestAnAdmissionFailureWhoseExitCannotEndTheTokenSaysSo(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"error\":\"the conversation does not exist\"}'\nexit 1\n")
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: failingRevoke}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "does not exist") || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("an admission failure whose exit revoke failed: %v", err)
	}
	if !coderhost.TurnStarted(err) {
		t.Error("an admission failure after the process was created is not reported as a started turn")
	}
}

func TestAHostInsideATurnWhoseRevokeFailsSaysSo(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nsleep 5\n")
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: failingRevoke, revokeTurn: endsOwnTokens,
		bind: func(string, string, string, int, string) error { return coderhost.ErrHostInsideTurn }}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("host inside a turn, revoke failed: %v", err)
	}
}

func TestABoundDeliveryFailureWhoseRevokeFailsSaysSo(t *testing.T) {
	r := newBoundRig(t, "")
	r.l.revoke, r.l.revokeTurn = failingRevoke, endsOwnTokens
	r.l.bound.afterMint = func() { // the token is revoked between the mint and the activation: the undo has to end it again
		if err := r.store.RevokeSessionTokensErr("default", "c1"); err != nil {
			t.Error(err)
		}
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("a bound delivery failure whose revoke failed: %v", err)
	}
}

func TestALeaderThatDiesBeforeTheMintCommitsAndWhoseRevokeFailsSaysSo(t *testing.T) {
	script := `d=$(dirname "$0")
sleep 300 <&3 &
echo $! > "$d/holder"
echo $$ > "$d/pid"
cat <&3 > "$d/frame"
while [ ! -e "$d/release" ]; do sleep 0.05; done
`
	r := newBoundRig(t, script)
	t.Cleanup(func() {
		if b, err := os.ReadFile(filepath.Join(r.dir, "holder")); err == nil {
			if hp, _ := strconv.Atoi(strings.TrimSpace(string(b))); hp > 1 {
				_ = syscall.Kill(hp, syscall.SIGKILL)
			}
		}
	})
	r.l.revoke, r.l.revokeTurn = failingRevoke, endsOwnTokens
	exited := make(chan struct{})
	var once sync.Once
	r.l.afterExitRevoke = func() { once.Do(func() { close(exited) }) }
	r.l.bound.beforeMint = func() {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(r.waitFile(filepath.Join(r.dir, "pid")))))
		_ = syscall.Kill(pid, syscall.SIGKILL)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("the exit goroutine never ran")
		}
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("a leader that died before the commit, revoke failing: %v", err)
	}
}

// A cleanup that has not finished when the start gives up waiting is a possibly-live token, not a clean failure.
func TestAStartWhoseCleanupOutlastsTheWaitSaysTheTokenMayBeLive(t *testing.T) {
	oldWait := hostTurnWait
	hostTurnWait = func() time.Duration { return 150 * time.Millisecond }
	t.Cleanup(func() { hostTurnWait = oldWait })
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nsleep 30\n")
	release := make(chan struct{}, 1)
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens:     func(string, string, string) (string, error) { return "mst-x", nil },
		revoke:     func(string, string) error { return nil },
		revokeTurn: func(string, string, string) error { <-release; return nil }, // the exit's cleanup hangs
		bind:       func(string, string, string, int, string) error { return errors.New("the token could not be bound") }}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) || !strings.Contains(err.Error(), "could not be bound") {
		t.Fatalf("a start whose cleanup had not finished: %v", err)
	}
	// the failure also says when the old turn is finished: not yet, and once its cleanup is released, yes
	pending := coderhost.TurnPending(err)
	if pending == nil {
		t.Fatal("the failure names no pending turn")
	}
	select {
	case <-pending:
		t.Fatal("the old turn is reported finished while its cleanup hangs")
	default:
	}
	release <- struct{}{}
	select {
	case <-pending:
	case <-time.After(5 * time.Second):
		t.Fatal("the old turn was never reported finished")
	}
}

// The launcher records the process the moment it exists, before admission; a record that cannot be written fails the start and the
// process is stopped (a process whose identity cannot be kept is not allowed to run). This is a behaviour change of the ordinary
// launcher: a failing session store now fails a start that used to succeed.
func TestTheLauncherRecordsTheProcessFirstAndStopsItWhenItCannot(t *testing.T) {
	bin, dir := scriptBin(t, "echo $$ > \"$(dirname \"$0\")/pid\"\necho '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nsleep 30\n")
	var recordedPID int
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: func(string, string) error { return nil },
		onProcess: func(_, _, turn string, pid int, stamp string, _ bool) error {
			recordedPID = pid
			if stamp == "" || turn != "t1" {
				t.Errorf("stamp %q turn %q", stamp, turn)
			}
			return errors.New("the session store is unavailable")
		}}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "could not record the turn's process") {
		t.Fatalf("a start whose process could not be recorded: %v", err)
	}
	if recordedPID == 0 {
		t.Fatal("the record was never asked for")
	}
	for i := 0; i < 300 && syscall.Kill(recordedPID, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(recordedPID, 0) == nil {
		_ = syscall.Kill(recordedPID, syscall.SIGKILL)
		t.Error("the process whose identity could not be recorded is still running")
	}
}

// A process whose identity cannot be recorded is killed AT ONCE, not asked to leave: a child that ignores SIGTERM is dead too, long
// before the stop's grace period (which is an hour here) would have killed it.
func TestAProcessThatCannotBeRecordedIsKilledEvenWhenItIgnoresTerm(t *testing.T) {
	oldGrace := hostTurnGrace
	hostTurnGrace = time.Hour
	t.Cleanup(func() { hostTurnGrace = oldGrace })
	bin, dir := scriptBin(t, "trap '' TERM\necho $$ > \"$(dirname \"$0\")/pid\"\nwhile true; do sleep 0.05; done\n")
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: func(string, string) error { return nil },
		onProcess: func(_, _, _ string, pid int, _ string, _ bool) error {
			for i := 0; i < 200; i++ { // the script writes its pid before it is the process the test looks at: wait for the trap to be set
				if _, err := os.Stat(filepath.Join(dir, "pid")); err == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			return errors.New("the session store is unavailable")
		}}
	start := time.Now()
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "could not record the turn's process") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the start waited %v for a process that ignores SIGTERM", time.Since(start))
	}
	b, _ := os.ReadFile(filepath.Join(dir, "pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 {
		t.Fatal("the script never wrote its pid")
	}
	for i := 0; i < 200 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("a process that ignores SIGTERM survived the failed record")
	}
}

// A bound launch is recorded as bound (with the plain stamp), an ordinary one is not.
func TestABoundLaunchIsRecordedAsBound(t *testing.T) {
	r := newBoundRig(t, "")
	var recorded string
	var recordedBound bool
	r.l.onProcess = func(_, _, _ string, _ int, stamp string, bound bool) error {
		recorded, recordedBound = stamp, bound
		return nil
	}
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if !recordedBound || !strings.HasPrefix(recorded, "start:") {
		t.Errorf("a bound launch was recorded as bound=%v with the stamp %q", recordedBound, recorded)
	}
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nexit 0\n")
	var plainBound = true
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		revoke: func(string, string) error { return nil }, onProcess: func(_, _, _ string, _ int, _ string, bound bool) error { plainBound = bound; return nil }}
	pt, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = pt.Wait()
	if plainBound {
		t.Error("an ordinary launch was recorded as bound")
	}
}

// A bound turn's leader exits naturally leaving a child in its process group, with no Stop ever coming: the child is killed with the
// exit (token revocation does not stop processes), so no later turn of the conversation can start beside it.
func TestABoundLeadersChildrenDieWithItsExitEvenWithoutAStop(t *testing.T) {
	script := `d=$(dirname "$0")
sleep 300 </dev/null >/dev/null 2>&1 &
echo $! > "$d/child"
echo $$ > "$d/pid"
cat <&3 > "$d/frame"
while [ ! -e "$d/release" ]; do sleep 0.05; done
`
	r := newBoundRig(t, script)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var child int
	for i := 0; i < 300 && child == 0; i++ {
		if b, rerr := os.ReadFile(filepath.Join(r.dir, "child")); rerr == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 || syscall.Kill(child, 0) != nil {
		t.Fatalf("the turn's child is not running (pid %d)", child)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if err := os.WriteFile(r.release, []byte("x"), 0o600); err != nil { // the leader exits on its own
		t.Fatal(err)
	}
	_ = turn.Wait()
	for i := 0; i < 300 && syscall.Kill(child, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(child, 0) == nil {
		t.Error("a child of a bound turn outlived its leader's exit")
	}
}

// A turn that has already exited (still unreaped, so a zombie) when the launcher reads its start time is identified all the same and
// ends with its real status, instead of being failed as one that "could not be identified".
func TestATurnThatExitedBeforeItsStartTimeWasReadIsStillIdentified(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nexit 0\n")
	old := stampOf
	stampOf = func(pid int) string { // the read comes after the exit: wait until the child is a zombie
		for i := 0; i < 500 && processState(pid) != "Z"; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		return old(pid)
	}
	t.Cleanup(func() { stampOf = old })
	var recorded string
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		revoke: func(string, string) error { return nil }, onProcess: func(_, _, _ string, _ int, stamp string, _ bool) error { recorded = stamp; return nil }}
	turn, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatalf("a turn that exited at once was refused: %v", err)
	}
	_ = turn.Wait()
	if !strings.HasPrefix(recorded, "start:") {
		t.Errorf("the exited turn was recorded with the stamp %q", recorded)
	}
}

// When the hold on a bound leader fails (waitid), Start's turn is ended: the leader and what it left in its group are dead.
func TestABoundTurnWhoseHoldFailsIsKilledWithItsGroup(t *testing.T) {
	old := holdLeader
	var dir atomic.Value          // the rig's directory, set before Start
	holdLeader = func(int) bool { // fails only once the script has forked its child, so the group has a member to kill
		d, _ := dir.Load().(string)
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(filepath.Join(d, "child")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	t.Cleanup(func() { holdLeader = old })
	script := `d=$(dirname "$0")
sleep 300 </dev/null >/dev/null 2>&1 &
echo $! > "$d/child"
echo $$ > "$d/pid"
cat <&3 > "$d/frame"
while :; do sleep 0.05; done
`
	r := newBoundRig(t, script)
	dir.Store(r.dir)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil && turn != nil {
		done := make(chan struct{})
		go func() { _ = turn.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the turn kept running after its hold failed")
		}
	}
	var child int
	for i := 0; i < 300 && child == 0; i++ {
		if b, rerr := os.ReadFile(filepath.Join(r.dir, "child")); rerr == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the child never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	for i := 0; i < 300 && syscall.Kill(child, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(child, 0) == nil {
		t.Error("the group's child outlived a turn whose hold failed")
	}
}

// A bound leader that exits on its own, leaving a child in its group, before a failed delivery is noticed: the group is dead when the
// start returns its error (the exit's settle killed it while the leader was still held, and the failed delivery's stop does not depend on it).
func TestAFailedBoundDeliveryAfterTheLeaderExitedLeavesNoGroupMemberRunning(t *testing.T) {
	script := `d=$(dirname "$0")
sleep 300 </dev/null >/dev/null 2>&1 &
echo $! > "$d/child"
echo $$ > "$d/pid"
exit 0
`
	r := newBoundRig(t, script)
	var child int
	r.l.bound.afterMint = func() { // the leader has exited by now: wait for it, then fail the delivery
		for i := 0; i < 500; i++ {
			if b, err := os.ReadFile(filepath.Join(r.dir, "child")); err == nil {
				child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			if pb, err := os.ReadFile(r.pidfile); err == nil {
				if pid, _ := strconv.Atoi(strings.TrimSpace(string(pb))); pid > 1 && processState(pid) == "Z" && child > 1 {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := r.store.RevokeSessionTokensErr("default", "c1"); err != nil {
			t.Error(err)
		}
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil {
		t.Fatal("the delivery did not fail")
	}
	if child <= 1 {
		t.Fatal("the leader never started its child")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	for i := 0; i < 300 && syscall.Kill(child, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(child, 0) == nil {
		t.Error("a member of the leader's group outlived a failed bound delivery whose leader had already exited")
	}
}

// The legacy token is bound with the stamp read BEFORE the exit waiter could reap the child: a turn that has already exited and been
// reaped by the time the binding is made is still bound with its own start stamp, never with an empty one or with whatever the number
// names afterwards.
func TestTheLegacyTokenIsBoundWithTheStampReadBeforeTheWaiterStarted(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nexit 0\n")
	exited := make(chan struct{})
	var once sync.Once
	old := afterWaiterStarted
	afterWaiterStarted = func() { // the child has exited and been reaped before the binding is made
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	}
	t.Cleanup(func() { afterWaiterStarted = old })
	var recorded, bound string
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		revoke:          func(string, string) error { return nil },
		afterExitRevoke: func() { once.Do(func() { close(exited) }) },
		onProcess:       func(_, _, _ string, _ int, stamp string, _ bool) error { recorded = stamp; return nil },
		bind:            func(_, _, _ string, _ int, stamp string) error { bound = stamp; return nil }}
	turn, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = turn.Wait()
	if !strings.HasPrefix(bound, "start:") {
		t.Fatalf("the token was bound with the stamp %q after the child was reaped", bound)
	}
	if !strings.HasPrefix(recorded, bound) { // the recorded stamp is "start:N@boot": the same start tick
		t.Errorf("the token was bound with %q, the process was recorded as %q", bound, recorded)
	}
}
