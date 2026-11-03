package doomsday

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/store"
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

// slowAudit blocks the audit append of a started ticket until released.
type slowAudit struct {
	hostStore
	entered chan struct{}
	release chan struct{}
}

func (s *slowAudit) AppendAudit(ctx context.Context, profile string, gen int64, kind, payload string) (store.AuditRow, error) {
	if kind == "ticket.started" {
		close(s.entered)
		<-s.release
	}
	return s.hostStore.AppendAudit(ctx, profile, gen, kind, payload)
}

// The registry write of a started ticket is not made under the gate's lock: while it is slow, other dispatches and the
// run-state reads proceed. Mutant tried: hold g.mu across Started again: red (the second dispatch never returns).
func TestBeforeReleaseDoesNotHoldTheGateLockAcrossTheAuditWrite(t *testing.T) {
	r := newHostRig(t)
	r.start(t)
	slow := &slowAudit{hostStore: r.gate.Store, entered: make(chan struct{}), release: make(chan struct{})}
	r.gate.Store = slow
	first := make(chan error, 1)
	go func() { first <- r.gate.BeforeRelease(context.Background(), "p1", "conv", "slow-1", true) }()
	<-slow.entered
	other := make(chan error, 1)
	go func() { _, err := r.gate.current(context.Background()); other <- err }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("another dispatch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a dispatch queued behind the audit write of another turn's ticket")
	}
	close(slow.release)
	if err := <-first; err != nil {
		t.Fatalf("the slow turn: %v", err)
	}
}

// gatedAudit makes the audit append of a started ticket wait (when hold is set) and then fail (while fail is set).
type gatedAudit struct {
	hostStore
	entered chan struct{}
	hold    chan struct{}
	fail    atomic.Bool
	once    atomic.Bool
}

func (s *gatedAudit) AppendAudit(ctx context.Context, profile string, gen int64, kind, payload string) (store.AuditRow, error) {
	if kind == "ticket.started" {
		if s.hold != nil && s.once.CompareAndSwap(false, true) {
			close(s.entered)
			<-s.hold
		}
		if s.fail.Load() {
			return store.AuditRow{}, errors.New("audit write failed")
		}
	}
	return s.hostStore.AppendAudit(ctx, profile, gen, kind, payload)
}

// A repeat of a turn id that is still taking its ticket waits for that call and gets its outcome: it is never allowed ahead
// of a first call that still fails. Mutant tried: the old rule (the id marked held before Started returns, a repeat returns
// nil at once): red, the repeat returns nil while the first fails.
func TestConcurrentDuplicateReleaseSharesTheFirstOutcome(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	slow := &gatedAudit{hostStore: r.gate.Store, entered: make(chan struct{}), hold: make(chan struct{})}
	slow.fail.Store(true)
	r.gate.Store = slow
	first := make(chan error, 1)
	go func() { first <- r.gate.BeforeRelease(context.Background(), "p1", "conv", "dup-1", true) }()
	<-slow.entered
	second := make(chan error, 1)
	go func() { second <- r.gate.BeforeRelease(context.Background(), "p1", "conv", "dup-1", true) }()
	select {
	case err := <-second:
		t.Fatalf("the repeat returned (%v) while the first call was still taking its ticket", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(slow.hold)
	e1, e2 := <-first, <-second
	if !refused(e1) || !refused(e2) {
		t.Fatalf("first = %v, repeat = %v: both must be the refusal", e1, e2)
	}
}

// A refused turn holds nothing: the same turn id asked again is a fresh decision. Mutant tried: no delete of the held entry
// after a failed Started: red (the retry gets the old refusal). The failed ticket is recorded unknown, never left as a
// starter (the Close outcome has no starter), which is what gate.Abort in the failure path guarantees.
func TestRefusedReleaseHoldsNothingAndLeavesNoStarter(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	bad := &gatedAudit{hostStore: r.gate.Store}
	bad.fail.Store(true)
	r.gate.Store = bad
	if err := r.gate.BeforeRelease(context.Background(), "p1", "conv", "again", true); !refused(err) {
		t.Fatalf("a failed audit write released the turn: %v", err)
	}
	gate, err := r.gate.current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o := gate.Outcome(); len(o.Starters) != 0 {
		t.Fatalf("a refused turn left a starter: %+v", o)
	}
	bad.fail.Store(false)
	if err := r.gate.BeforeRelease(context.Background(), "p1", "conv", "again", true); err != nil {
		t.Fatalf("the retry of a refused turn: %v", err)
	}
	if n := r.tickets(t); n != 1 {
		t.Fatalf("%d ticket(s) after the retry, want 1", n)
	}
}

// A late Started (the gate closed while the registry write was slow) refuses the turn and records the ticket aborted.
func TestReleaseOfATicketThatTurnsLateIsRefused(t *testing.T) {
	r := newHookRig(t, on())
	r.start(t)
	slow := &gatedAudit{hostStore: r.gate.Store, entered: make(chan struct{}), hold: make(chan struct{})}
	r.gate.Store = slow
	first := make(chan error, 1)
	go func() { first <- r.gate.BeforeRelease(context.Background(), "p1", "conv", "late-2", true) }()
	<-slow.entered
	gate, err := r.gate.current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate.Seal()
	close(slow.hold)
	if err := <-first; !refused(err) {
		t.Fatalf("a ticket that turned late released its turn: %v", err)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'ticket.aborted'`); n != 1 {
		t.Fatalf("%d aborted record(s), want 1", n)
	}
}
