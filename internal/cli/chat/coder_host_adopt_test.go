//go:build linux || darwin

package chat

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// The session unit (macOS) is joined AFTER the turn's process exists: the launcher asks its adopt hook right after the process
// is started and identified, before the identity is committed, before the release check and before anything is delivered. A
// failed adoption ends the child (it has been given nothing); a leader the unit holds is never reaped by the launcher.

// holdRig is the test's stand-in for the unit's hold: it counts the leaders handed to it and reaps them only when released,
// the way the real hold does (stop.SessionOS.Hold).
type holdRig struct {
	mu       sync.Mutex
	pids     []int
	released bool
}

func (h *holdRig) adopt(held bool, err error, log *[]string) func(string, string, int, string) (bool, error) {
	return func(_, _ string, pid int, stamp string) (bool, error) {
		h.mu.Lock()
		h.pids = append(h.pids, pid)
		h.mu.Unlock()
		if log != nil {
			*log = append(*log, "adopt")
		}
		return held, err
	}
}

// reap is what the hold's release does: wait for the leader (it is the caller's own child).
func (h *holdRig) reap(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, pid := range h.pids {
		var ws syscall.WaitStatus
		for {
			_, err := syscall.Wait4(pid, &ws, 0, nil)
			if err != syscall.EINTR {
				break
			}
		}
	}
	h.released = true
}

func unreapedZombie(pid int) bool {
	for i := 0; i < 300; i++ {
		if processState(pid) == "Z" {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// still is true while pid is a zombie for the whole of d (nobody reaped it).
func stillZombie(pid int, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if processState(pid) != "Z" {
			return false
		}
	}
	return true
}

func noSuchProcess(pid int) bool {
	for i := 0; i < 300; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestAdoptRunsAfterTheStartAndBeforeTheReleaseCheckAndTheDelivery(t *testing.T) {
	r := newBoundRig(t, "")
	var h holdRig
	var log []string
	r.l.place = func(*exec.Cmd, string, string) (func(), error) { log = append(log, "place"); return func() {}, nil }
	r.l.adopt = h.adopt(false, nil, &log)
	r.l.onProcess = func(_, _, _ string, _ int, _ string, _ bool) error { log = append(log, "identity"); return nil }
	r.l.beforeRelease = func(_ context.Context, _, _, _ string, _ bool) error {
		if b, _ := os.ReadFile(r.frame); len(b) != 0 {
			t.Error("the credentials were delivered before the release check")
		}
		log = append(log, "release-check")
		return nil
	}
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitFile(r.frame)
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if got := strings.Join(log, ","); got != "place,adopt,identity,release-check" {
		t.Errorf("order %s", got)
	}
	if len(h.pids) != 1 || h.pids[0] != turn.PID() {
		t.Errorf("the hook was asked about %v, the turn is %d", h.pids, turn.PID())
	}
}

// A failed adoption: nothing was given to the child, it is dead (and, since nothing holds it, reaped by the launcher), the start
// failed as a started turn, no release check ran, and the turn's tokens are gone.
func TestAFailedAdoptionKillsTheChildAndDeliversNothing(t *testing.T) {
	r := newBoundRig(t, "")
	var h holdRig
	var committed atomic.Int64
	r.l.onProcess = func(_, _, _ string, pid int, _ string, _ bool) error { committed.Store(int64(pid)); return nil }
	r.l.adopt = h.adopt(false, errors.New("no ledger"), nil)
	r.l.beforeRelease = func(context.Context, string, string, string, bool) error {
		t.Error("the release check ran for a turn that was not adopted")
		return nil
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "no ledger") {
		t.Fatalf("a refused adoption: %v", err)
	}
	if !coderhost.TurnStarted(err) {
		t.Error("the process existed: the failure is a started turn")
	}
	if len(h.pids) != 1 {
		t.Fatalf("the hook was asked %d times", len(h.pids))
	}
	pid := h.pids[0]
	if !noSuchProcess(pid) {
		t.Errorf("the refused turn's process %d is neither gone nor reaped by the launcher (state %q)", pid, processState(pid))
	}
	if b, _ := os.ReadFile(r.frame); len(b) != 0 {
		t.Errorf("an unadopted turn was given its credentials (%d bytes)", len(b))
	}
	if toks, binds := r.v2Rows(); toks != 0 || binds != 0 {
		t.Errorf("a token was minted for an unadopted turn: %d tokens, %d bindings", toks, binds)
	}
	if got := int(committed.Load()); got != 0 {
		t.Errorf("the identity was committed (%d) for a turn that was not adopted", got)
	}
}

// A leader the unit took over (the hold took it and its release reaps it) is not reaped by the launcher even when the adoption
// then failed: reaping twice could take a recycled number.
func TestARefusedAdoptionThatTookTheHoldIsNotReapedByTheLauncher(t *testing.T) {
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(true, errors.New("witness full"), nil)
	if _, err := r.l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "witness full") {
		t.Fatalf("%v", err)
	}
	if len(h.pids) == 0 {
		t.Fatal("the adopt hook was never asked")
	}
	pid := h.pids[0]
	if !unreapedZombie(pid) {
		t.Fatalf("the launcher reaped a leader the unit holds (state %q)", processState(pid))
	}
	if !stillZombie(pid, 300*time.Millisecond) {
		t.Fatal("the leader was reaped behind the hold's back")
	}
	h.reap(t) // the hold's release
	if !noSuchProcess(pid) {
		t.Error("the release did not reap the leader")
	}
	if b, _ := os.ReadFile(r.frame); len(b) != 0 {
		t.Errorf("credentials were delivered (%d bytes)", len(b))
	}
}

// The turn ends when its held leader exits, its tokens end with it, and the leader stays an unreaped zombie (its number stays
// reserved) until the unit lets go.
func TestAHeldLeaderIsNotReapedWhenItsTurnEnds(t *testing.T) {
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(true, nil, nil)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitFile(r.frame)
	var id string
	if err := r.db.DB.QueryRow(`SELECT id FROM coder_session_tokens_v2`).Scan(&id); err != nil {
		t.Fatalf("the live turn has no token: %v", err)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	done := make(chan error, 1)
	go func() { done <- turn.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the held turn's end: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the turn of a held leader never ended")
	}
	pid := turn.PID()
	if !unreapedZombie(pid) {
		t.Fatalf("the held leader was reaped by the launcher (state %q)", processState(pid))
	}
	if !stillZombie(pid, 300*time.Millisecond) {
		t.Fatal("the held leader was reaped later")
	}
	// the exit's revoke ran before the turn was done
	if rv, err := procbind.IsRevoked(context.Background(), r.db.DB, procbind.KindSession, id); err != nil || !rv {
		t.Errorf("the token survived the held turn: %v %v", rv, err)
	}
	if l, ok := r.binder.Acquire(id, "default", procbind.SessionScope("c1", "t1")); ok {
		l.Release()
		t.Error("the registration survived the held turn")
	}
	h.reap(t)
	if !noSuchProcess(pid) {
		t.Error("the release did not reap the leader")
	}
}

// A leader that is not held is reaped by the launcher exactly as before (no zombie is left by an ordinary turn).
func TestAnUnheldLeaderIsReapedAsBefore(t *testing.T) {
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(false, nil, nil)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitFile(r.frame)
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if !noSuchProcess(turn.PID()) {
		t.Errorf("an unheld leader was left unreaped (state %q)", processState(turn.PID()))
	}
}

// A leader whose exit cannot be observed without reaping it is killed and the turn still ends, without a reap.
func TestAHeldLeaderWhoseExitCannotBeObservedIsKilledNotReaped(t *testing.T) {
	old := holdLeader
	holdLeader = func(int) bool { return false }
	t.Cleanup(func() { holdLeader = old })
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = h.adopt(true, nil, nil)
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	done := make(chan struct{})
	if err == nil {
		go func() { _ = turn.Wait(); close(done) }()
	} else {
		close(done)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("the turn never ended (start: %v)", err)
	}
	if len(h.pids) == 0 {
		t.Fatal("the adopt hook was never asked")
	}
	pid := h.pids[0]
	if !unreapedZombie(pid) {
		t.Fatalf("the leader is not an unreaped zombie (state %q): killed and reaped, or still running", processState(pid))
	}
	h.reap(t)
}

// An adopt hook that panics is a failed start with the child stopped, and the child is not reaped (nobody can say whether the
// hold was taken before the panic).
func TestAnAdoptHookThatPanicsFailsTheStartAndStopsTheChild(t *testing.T) {
	r := newBoundRig(t, "")
	var h holdRig
	r.l.adopt = func(_, _ string, pid int, _ string) (bool, error) {
		h.pids = append(h.pids, pid)
		panic("boom")
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("%v", err)
	}
	if len(h.pids) != 1 || !unreapedZombie(h.pids[0]) {
		t.Fatalf("the child of a panicked adoption: pids %v", h.pids)
	}
	h.reap(t)
	if b, _ := os.ReadFile(r.frame); len(b) != 0 {
		t.Errorf("credentials were delivered (%d bytes)", len(b))
	}
}

// No hook, no adoption: the launch is what it was. A legacy (unbound) start never asks.
func TestNoAdoptHookAndALegacyStartAreUnchanged(t *testing.T) {
	r := newBoundRig(t, "")
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r.waitFile(r.frame)
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()

	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-monoagentcli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := 0
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: dir + "/turns", revokeBackoff: time.Millisecond,
		revoke: func(string, string) error { return nil },
		adopt:  func(_, _ string, _ int, _ string) (bool, error) { called++; return true, nil }}
	lt, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = lt.Wait()
	if called != 0 {
		t.Errorf("a legacy launch asked the adopt hook %d times", called)
	}
}

// InstallRunHooks puts the adopt hook on the production launchers.
func TestInstallRunHooksSetsTheAdoptHook(t *testing.T) {
	legacy, bound := &execLauncher{}, &execLauncher{}
	h := &coderhost.Host{L: &gatedLauncher{inner: legacy, bound: bound}}
	hooks := RunHooks{Adopt: func(string, string, int, string) (bool, error) { return false, nil }}
	if err := InstallRunHooks(coderhost.NewStore(nil), h, hooks); err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]*execLauncher{"legacy": legacy, "bound": bound} {
		if l.adopt == nil {
			t.Errorf("%s launcher without the adopt hook", name)
		}
	}
}
