package doomsday

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// hookLauncher stands in for the production launcher: it makes the "process" and then asks the release hook before
// delivering anything, as chat's execLauncher does. A refused release stops the turn and fails the start.
type hookLauncher struct {
	fakeLauncher
	hk       Hooks
	bound    bool
	ctx      context.Context
	released int
	stopped  int
}

func (l *hookLauncher) Start(profileID, conv, turnID, msg string, _ []string, _ string) (coderhost.Turn, error) {
	l.starts++
	t := &fakeTurn{}
	if l.hk.BeforeRelease != nil {
		ctx := l.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := l.hk.BeforeRelease(ctx, profileID, conv, turnID, l.bound); err != nil {
			l.stopped++
			return nil, &coderhost.StartedError{Err: err}
		}
	}
	l.released++
	return t, nil
}

type hookRig struct {
	*hostRig
	hl *hookLauncher
}

func newHookRig(t *testing.T, binding *atomic.Bool) *hookRig {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	hl := &hookLauncher{bound: true}
	h := &coderhost.Host{Store: w.host, L: hl}
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir,
		BindingOn:    func() (bool, error) { return binding.Load(), nil },
		InstallHooks: func(_ *coderhost.Store, _ *coderhost.Host, hk Hooks) error { hl.hk = hk; return nil }}
	if err := g.Install(w.host, h); err != nil {
		t.Fatal(err)
	}
	return &hookRig{hostRig: &hostRig{wireRig: w, db: db, l: &hl.fakeLauncher, h: h, gate: g}, hl: hl}
}

func (r *hookRig) tickets(t *testing.T) int {
	t.Helper()
	return count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'ticket.started'`)
}

func on() *atomic.Bool { b := &atomic.Bool{}; b.Store(true); return b }

// One ticket per turn: the release hook takes it, the wrapper around Start does not take a second one.
func TestHookedTurnTakesExactlyOneTicketAtRelease(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	if err := r.startTurn(); err != nil || r.hl.released != 1 {
		t.Fatalf("turn: %v released=%d", err, r.hl.released)
	}
	if n := r.tickets(t); n != 1 {
		t.Fatalf("%d ticket(s) for one turn, want 1", n)
	}
}

// The check finished after the launcher gave up (ctx cancelled): nothing is consumed. Mutant tried: drop the ctx.Err() check
// under the lock in BeforeRelease: this test is red (a ticket is registered for a turn that was refused).
func TestLateCompletionForARefusedTurnConsumesNothing(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.gate.BeforeRelease(ctx, "p1", "conv", "late-1", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a late check was not refused by its context: %v", err)
	}
	if n := r.tickets(t); n != 0 {
		t.Fatalf("a late completion consumed %d ticket(s)", n)
	}
	// the same turn, asked again with a live context, is a fresh decision (the first one consumed nothing)
	if err := r.gate.BeforeRelease(context.Background(), "p1", "conv", "late-1", true); err != nil || r.tickets(t) != 1 {
		t.Fatalf("retry: %v tickets=%d", err, r.tickets(t))
	}
}

// A check cancelled while another holds the gate lock consumes nothing (the same ctx check; this is the ordering the launcher's
// deadline produces). Mutant tried: no ctx check: red.
func TestCancelWhileWaitingForTheGateLockConsumesNothing(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.gate.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- r.gate.BeforeRelease(ctx, "p1", "conv", "wait-1", true) }()
	waitBlocked(t)
	cancel()
	r.gate.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if n := r.tickets(t); n != 0 {
		t.Fatalf("%d ticket(s) consumed after the context was cancelled", n)
	}
}

// waitBlocked gives the goroutine time to queue on g.mu; the test holds the lock, so it cannot get past it.
func waitBlocked(t *testing.T) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
}

// A turn id holds one ticket however often the check is asked. Mutant tried: no released-set: two tickets, red.
func TestReleaseIsIdempotentPerTurnID(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	for i := 0; i < 3; i++ {
		if err := r.gate.BeforeRelease(context.Background(), "p1", "conv", "same", true); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.tickets(t); n != 1 {
		t.Fatalf("%d tickets for one turn id", n)
	}
	if err := r.gate.BeforeRelease(context.Background(), "p1", "conv", "other", true); err != nil || r.tickets(t) != 2 {
		t.Fatalf("a different turn must get its own ticket: %v %d", err, r.tickets(t))
	}
}

// No run without binding, at the release hook: a turn that is not bound is refused after its process exists.
// Mutant tried: drop the `!bound` refusal in BeforeRelease: red.
func TestUnboundTurnIsRefusedAtReleaseWhileARunIsOpen(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	r.hl.bound = false
	err := r.startTurn()
	if !refused(err) || !strings.Contains(err.Error(), "no run without binding") || r.hl.released != 0 {
		t.Fatalf("an unbound turn was released: %v", err)
	}
	if n := r.tickets(t); n != 0 {
		t.Fatalf("%d ticket(s) for a refused turn", n)
	}
}

// ... and before Start, by the wrapper, while the setting is off: no process is created at all.
// Mutant tried: drop requireBound in HostGate.Start: red.
func TestWrapperRefusesAnUnboundDispatchBeforeStart(t *testing.T) {
	off := &atomic.Bool{}
	r := newHookRig(t, off)
	r.start(t)
	err := r.startTurn()
	if !refused(err) || !strings.Contains(err.Error(), "no run without binding") {
		t.Fatalf("a turn start with binding off was not refused: %v", err)
	}
	if r.hl.starts != 0 {
		t.Fatalf("%d process(es) were created before the refusal", r.hl.starts)
	}
	off.Store(true)
	if err := r.startTurn(); err != nil {
		t.Fatalf("with binding on the turn starts: %v", err)
	}
}

func TestUnknownOrUnreadableBindingRefuses(t *testing.T) {
	for name, fn := range map[string]func() (bool, error){
		"nil":    nil,
		"error":  func() (bool, error) { return true, errors.New("settings locked") },
		"absent": func() (bool, error) { return false, nil },
	} {
		r := newHookRig(t, on())
		r.gate.BindingOn = fn
		r.start(t)
		if err := r.startTurn(); !refused(err) || r.hl.starts != 0 {
			t.Fatalf("%s: a turn started (%v)", name, err)
		}
	}
}

// With no open run the binding setting is irrelevant and nothing is ticketed: the host is what it was.
func TestNoRunMeansNoBindingRequirementAndNoTicket(t *testing.T) {
	off := &atomic.Bool{}
	r := newHookRig(t, off)
	r.hl.bound = false
	if err := r.startTurn(); err != nil || r.hl.released != 1 {
		t.Fatalf("a turn was refused with no run: %v", err)
	}
	if n := r.tickets(t); n != 0 {
		t.Fatalf("%d ticket(s) with no run", n)
	}
	if id, gen := r.gate.RunStamp("p1", "conv"); id != "" || gen != 0 {
		t.Fatalf("a stamp with no run: %q %d", id, gen)
	}
}

// The stamp supplier answers the open run, and nothing once it is closed to the profile.
func TestRunStampNamesTheOpenRun(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	id, gen := r.gate.RunStamp("p1", "conv")
	runs, err := r.w.Store.OpenRuns(context.Background(), "p1")
	if err != nil || len(runs) != 1 || id != runs[0].CharterID || id == "" || gen != 1 {
		t.Fatalf("stamp = %q %d, run = %+v (%v)", id, gen, runs, err)
	}
}

// A stopping run refuses the release and creates no process.
func TestStoppedRunRefusesReleaseAndStart(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	if _, err := r.w.RunDir.WriteMarker(1, "", "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.startTurn(); !refused(err) || r.hl.starts != 0 {
		t.Fatalf("a turn started on a stopping run: %v starts=%d", err, r.hl.starts)
	}
	if err := r.gate.BeforeRelease(context.Background(), "p1", "c", "x", true); !refused(err) {
		t.Fatalf("release on a stopping run: %v", err)
	}
}

// Hooks that cannot be installed: Install says so, and the gate still tickets the turn around Start (never ungated).
func TestInstallWithoutHooksFallsBackToTheTicketAroundStart(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	l := &fakeLauncher{}
	h := &coderhost.Host{Store: w.host, L: l}
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, BindingOn: func() (bool, error) { return true, nil },
		InstallHooks: func(*coderhost.Store, *coderhost.Host, Hooks) error { return errors.New("not the production launcher") }}
	if err := g.Install(w.host, h); err == nil {
		t.Fatal("Install hid the hook failure")
	}
	w.start(t)
	if _, err := h.L.Start("p1", "c", "t1", "hi", nil, ""); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'ticket.started'`); n != 1 {
		t.Fatalf("%d ticket(s) in the fallback, want 1", n)
	}
	if _, err := w.w.RunDir.WriteMarker(1, "", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.L.Start("p1", "c", "t2", "hi", nil, ""); !refused(err) {
		t.Fatalf("fallback let a turn start on a stopping run: %v", err)
	}
}
