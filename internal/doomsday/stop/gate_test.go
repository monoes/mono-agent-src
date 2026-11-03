package stop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func notYet(t *testing.T, ch <-chan Outcome, why string) {
	t.Helper()
	select {
	case o := <-ch:
		t.Fatalf("Close returned %v, but %s", o.Kind, why)
	case <-time.After(60 * time.Millisecond):
	}
}

func outcome(t *testing.T, ch <-chan Outcome) Outcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
		return Outcome{}
	}
}

// T3: an ADMITTED ticket paused before BeginStart is not waited for; after release BeginStart refuses.
func TestT3AdmittedTicketIsFencedAtBeginStart(t *testing.T) {
	f := newGateFixture(t)
	tk, _ := f.g.Admit(Spec{Kind: KindE1})
	pp := f.p.arm("beginstart.before")
	errc := make(chan error, 1)
	go func() { errc <- f.g.BeginStart(tk) }()
	pp.wait()
	o := f.g.Close()
	if o.Kind != Barrier {
		t.Fatalf("outcome %v, want BARRIER (an ADMITTED ticket is not a live starter)", o.Kind)
	}
	pp.resume()
	if err := <-errc; !errors.Is(err, ErrGateClosed) {
		t.Fatalf("BeginStart after Close = %v, want refusal", err)
	}
	if got := f.g.State(tk); got != Fenced {
		t.Fatalf("state %v, want FENCED", got)
	}
}

// Admit never succeeds once sealed, and does not wait behind a closer.
func TestAdmitRefusedAfterSeal(t *testing.T) {
	f := newGateFixture(t)
	f.g.Seal()
	if _, err := f.g.Admit(Spec{Kind: KindE1}); !errors.Is(err, ErrGateClosed) {
		t.Fatalf("Admit = %v", err)
	}
}

// T4: an E1 held child exists while the ticket is STARTING; Close waits, Started sees the closed gate, the
// held child is killed and never ran.
func TestT4HeldChildKilledWhenGateClosesMidStart(t *testing.T) {
	f := newGateFixture(t)
	held := &fakeHeld{}
	pp := f.p.arm("started.before-registry")
	type res struct {
		tk  *Ticket
		err error
	}
	rc := make(chan res, 1)
	go func() {
		tk, err := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
		rc <- res{tk, err}
	}()
	pp.wait()
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	notYet(t, cc, "a ticket is STARTING")
	pp.resume()
	r := <-rc
	if !errors.Is(r.err, ErrLate) {
		t.Fatalf("StartHeld = %v, want ErrLate", r.err)
	}
	if o := outcome(t, cc); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
	if !held.killed.Load() || held.released.Load() {
		t.Fatalf("killed=%v released=%v: the held child must be killed and never released", held.killed.Load(), held.released.Load())
	}
	if _, ok := f.reg.abortedApproval(r.tk.ID()); !ok {
		t.Fatal("the registry row was not marked aborted")
	}
}

// T5: a ticket stuck STARTING past T_close: CLOSED-WITH-STARTERS, no quiescence, recorded unknown.
func TestT5StuckStarterGivesClosedWithStarters(t *testing.T) {
	f := newGateFixture(t)
	tk := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	notYet(t, cc, "the deadline has not passed")
	f.clock.Advance(DefaultTClose)
	o := outcome(t, cc)
	if o.Kind != ClosedWithStarters || len(o.Starters) != 1 || o.Starters[0] != tk.ID() {
		t.Fatalf("outcome %+v", o)
	}
	f.reg.mu.Lock()
	n := len(f.reg.unknown)
	f.reg.mu.Unlock()
	if n != 1 {
		t.Fatalf("starters recorded unknown = %d, want 1", n)
	}
}

// T6/P1 of 13.1: a starter that resumes after the deadline creates a held process; the late Started kills it
// and the outcome improves only when it resolves (re-evaluation on resolution).
func TestT6LateStarterAfterDeadlineIsAborted(t *testing.T) {
	f := newGateFixture(t)
	held := &fakeHeld{}
	pp := f.p.arm("started.before-registry")
	rc := make(chan error, 1)
	go func() {
		_, err := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
		rc <- err
	}()
	pp.wait()
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	f.clock.Advance(DefaultTClose)
	if o := outcome(t, cc); o.Kind != ClosedWithStarters {
		t.Fatalf("outcome %v", o.Kind)
	}
	pp.resume()
	if err := <-rc; !errors.Is(err, ErrLate) {
		t.Fatalf("late start = %v", err)
	}
	if !held.killed.Load() {
		t.Fatal("late held child not killed")
	}
	if o := f.g.Outcome(); o.Kind != Barrier {
		t.Fatalf("published outcome after resolution = %v, want BARRIER", o.Kind)
	}
}

// T8: concurrent and repeated Close, Abort racing Close, double Abort: one result, no deadlock.
func TestT8ConcurrentCloseAndAbort(t *testing.T) {
	f := newGateFixture(t)
	tk := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	var wg sync.WaitGroup
	outs := make([]Outcome, 3)
	for i := range outs {
		wg.Add(1)
		go func() { defer wg.Done(); outs[i] = f.g.Close() }()
	}
	wg.Add(2)
	go func() { defer wg.Done(); f.g.Abort(tk) }()
	go func() { defer wg.Done(); f.g.Abort(tk) }()
	wg.Wait()
	for _, o := range outs {
		if o.Kind != Barrier {
			t.Fatalf("outcomes %+v", outs)
		}
	}
	if len(f.reg.aborted) != 1 {
		t.Fatalf("abort written %d times", len(f.reg.aborted))
	}
}

// T9/T33: a panic after BeginStart takes the STARTING -> ABORTING durable path, with the audit approval.
func TestT9PanicAfterBeginStartAbortsDurably(t *testing.T) {
	f := newGateFixture(t)
	held := &fakeHeld{}
	tk, err := f.g.StartHeld(Spec{ApprovalID: "appr-1"}, func() (Held, error) { panic("boom") })
	if !errors.Is(err, ErrPanic) {
		t.Fatalf("err %v", err)
	}
	if a, ok := f.reg.abortedApproval(tk.ID()); !ok || a != "appr-1" {
		t.Fatalf("durable abort missing or without approval: %q %v", a, ok)
	}
	if got := f.g.State(tk); got != Aborted {
		t.Fatalf("state %v", got)
	}
	_ = held
}

// T10/T24: a registry write failure after the process exists: killed, never released, ticket UNKNOWN.
func TestT10RegistryFailureKillsHeldChild(t *testing.T) {
	f := newGateFixture(t)
	f.reg.failStarted.Store(true)
	held := &fakeHeld{}
	tk, err := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
	if !errors.Is(err, ErrRegistryWrite) {
		t.Fatalf("err %v", err)
	}
	if !held.killed.Load() || held.released.Load() {
		t.Fatal("held child must be killed and never released")
	}
	if got := f.g.State(tk); got != Unknown {
		t.Fatalf("state %v, want UNKNOWN", got)
	}
	if o := f.g.Close(); o.Kind != ClosedWithUnknown {
		t.Fatalf("outcome %v, want CLOSED-WITH-UNKNOWN", o.Kind)
	}
}

// A kill that fails leaves the ticket UNKNOWN (run stays cleanup-pending).
func TestKillFailureLeavesUnknown(t *testing.T) {
	f := newGateFixture(t)
	f.reg.failStarted.Store(true)
	held := &fakeHeld{killErr: errInjected}
	tk, _ := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
	if f.g.State(tk) != Unknown {
		t.Fatalf("state %v", f.g.State(tk))
	}
}

// Happy path: release only after Started.
func TestStartHeldReleasesAfterStarted(t *testing.T) {
	f := newGateFixture(t)
	held := &fakeHeld{}
	tk, err := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
	if err != nil || !held.released.Load() || held.killed.Load() || f.g.State(tk) != Started {
		t.Fatalf("err=%v released=%v killed=%v state=%v", err, held.released.Load(), held.killed.Load(), f.g.State(tk))
	}
}

// T22: an E3 call in flight for many T_hung does not hold Close; the context is cancelled (T23).
func TestT22T23InFlightE3DoesNotBlockCloseAndIsCancelled(t *testing.T) {
	f := newGateFixture(t)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	go func() {
		_, _ = f.g.CallRemote(Spec{}, func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		})
	}()
	<-entered
	f.clock.Advance(10 * DefaultTHung)
	if o := f.g.Close(); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
	<-cancelled
	if got := f.g.HungStarters(); len(got) != 0 {
		t.Fatalf("a STARTED E3 call must not count as a hung starter: %v", got)
	}
}

// T23b: E3 paused STARTING at started.before-registry; Close; release: aborted, transport never entered.
func TestT23bE3AbortedWhenGateClosesBeforeStarted(t *testing.T) {
	f := newGateFixture(t)
	var entries atomic.Int32
	pp := f.p.arm("started.before-registry")
	rc := make(chan error, 1)
	go func() {
		_, err := f.g.CallRemote(Spec{}, func(context.Context) error { entries.Add(1); return nil })
		rc <- err
	}()
	pp.wait()
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	pp.resume()
	if err := <-rc; !errors.Is(err, ErrLate) {
		t.Fatalf("err %v", err)
	}
	if o := outcome(t, cc); o.Kind != Barrier || entries.Load() != 0 {
		t.Fatalf("outcome %v entries %d", o.Kind, entries.Load())
	}
}

// T29a: Close cancels before the wrapper's context check: the transport is not entered.
func TestT29aCancelledBeforeCtxCheckNotEntered(t *testing.T) {
	f := newGateFixture(t)
	var entries atomic.Int32
	pp := f.p.arm("transport.before-ctx-check")
	rc := make(chan error, 1)
	go func() {
		_, err := f.g.CallRemote(Spec{}, func(context.Context) error { entries.Add(1); return nil })
		rc <- err
	}()
	pp.wait()
	if o := f.g.Close(); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
	pp.resume()
	if err := <-rc; !errors.Is(err, context.Canceled) || entries.Load() != 0 {
		t.Fatalf("err %v entries %d", err, entries.Load())
	}
}

// T29b: the pause is after the context read; BARRIER does not prevent the entry (admitted-before-Stop).
func TestT29bEnteredAfterBarrierIsAdmittedBeforeStop(t *testing.T) {
	f := newGateFixture(t)
	var entries atomic.Int32
	pp := f.p.arm("transport.after-ctx-check")
	rc := make(chan error, 1)
	go func() {
		_, err := f.g.CallRemote(Spec{}, func(context.Context) error { entries.Add(1); return nil })
		rc <- err
	}()
	pp.wait()
	if o := f.g.Close(); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
	pp.resume()
	<-rc
	if entries.Load() != 1 {
		t.Fatalf("entries %d, want 1 (pinned: BARRIER does not prevent a STARTED E3 entering)", entries.Load())
	}
}

// T29c: a wrapper run with Started returning late never calls the transport.
func TestT29cLateStartedNeverCallsTransport(t *testing.T) {
	f := newGateFixture(t)
	f.reg.failStarted.Store(true)
	var entries atomic.Int32
	_, err := f.g.CallRemote(Spec{}, func(context.Context) error { entries.Add(1); return nil })
	if !errors.Is(err, ErrRegistryWrite) || entries.Load() != 0 {
		t.Fatalf("err %v entries %d", err, entries.Load())
	}
}

// T30a/b/c, T30, T34: abort writes.
func TestT30AbortWriteSemantics(t *testing.T) {
	t.Run("close waits for the durable abort write", func(t *testing.T) {
		f := newGateFixture(t)
		held := &fakeHeld{}
		sp := f.p.arm("started.before-registry")
		go func() { _, _ = f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil }) }()
		sp.wait()
		ap := f.p.arm("abort.before-write")
		cc := closeAsync(f.g)
		waitSealed(t, f.g)
		sp.resume()
		ap.wait()
		notYet(t, cc, "the abort write has not finished")
		ap.resume()
		if o := outcome(t, cc); o.Kind != Barrier {
			t.Fatalf("outcome %v", o.Kind)
		}
	})
	t.Run("failed abort write is UNKNOWN then resolves", func(t *testing.T) {
		f := newGateFixture(t)
		f.reg.failAborted.Store(true)
		held := &fakeHeld{}
		sp := f.p.arm("started.before-registry")
		rc := make(chan *Ticket, 1)
		go func() {
			tk, _ := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
			rc <- tk
		}()
		sp.wait()
		cc := closeAsync(f.g)
		waitSealed(t, f.g)
		sp.resume()
		tk := <-rc
		if o := outcome(t, cc); o.Kind != ClosedWithUnknown {
			t.Fatalf("outcome %v, want CLOSED-WITH-UNKNOWN", o.Kind)
		}
		if f.g.State(tk) != Unknown || !held.killed.Load() {
			t.Fatalf("state %v killed %v", f.g.State(tk), held.killed.Load())
		}
		f.reg.failAborted.Store(false)
		f.g.RetryUnknown()
		if o := f.g.Outcome(); o.Kind != Barrier {
			t.Fatalf("after the write succeeds: %v", o.Kind)
		}
	})
	t.Run("deadline with ABORTING gives CLOSED-WITH-UNKNOWN (T34)", func(t *testing.T) {
		f := newGateFixture(t)
		held := &fakeHeld{}
		sp := f.p.arm("started.before-registry")
		go func() { _, _ = f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil }) }()
		sp.wait()
		ap := f.p.arm("abort.before-write")
		cc := closeAsync(f.g)
		waitSealed(t, f.g)
		sp.resume()
		ap.wait()
		f.clock.Advance(DefaultTClose)
		o := outcome(t, cc)
		if o.Kind != ClosedWithUnknown || len(o.Aborting) != 1 {
			t.Fatalf("outcome %+v", o)
		}
		ap.resume()
	})
	t.Run("an UNKNOWN from before Close is never BARRIER (T30c)", func(t *testing.T) {
		f := newGateFixture(t)
		f.reg.failStarted.Store(true)
		_, _ = f.g.StartHeld(Spec{}, func() (Held, error) { return &fakeHeld{}, nil })
		if o := f.g.Close(); o.Kind != ClosedWithUnknown {
			t.Fatalf("outcome %v", o.Kind)
		}
	})
}

// T31: a consumed approval whose ticket is aborted is audited.
func TestT31AbortedAfterConsumeCarriesApproval(t *testing.T) {
	f := newGateFixture(t)
	sp := f.p.arm("started.before-registry")
	rc := make(chan *Ticket, 1)
	go func() {
		tk, _ := f.g.CallRemote(Spec{ApprovalID: "ap-9"}, func(context.Context) error { return nil })
		rc <- tk
	}()
	sp.wait()
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	sp.resume()
	tk := <-rc
	outcome(t, cc)
	if a, _ := f.reg.abortedApproval(tk.ID()); a != "ap-9" {
		t.Fatalf("approval in abort row = %q", a)
	}
}

// T32: a duplicate BeginStart refuses and never fences a STARTING ticket; Close keeps waiting.
func TestT32DuplicateBeginStartDoesNotFence(t *testing.T) {
	f := newGateFixture(t)
	tk := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	if err := f.g.BeginStart(tk); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("second BeginStart = %v", err)
	}
	if f.g.State(tk) != Starting {
		t.Fatalf("state %v: a duplicate BeginStart must not change a STARTING ticket", f.g.State(tk))
	}
	notYet(t, cc, "the first caller is still STARTING")
	f.g.Abort(tk)
	outcome(t, cc)
}

// T40: a deadline with a ticket STARTING and another ABORTING lists both under CLOSED-WITH-STARTERS.
func TestT40StartersWinOverAborting(t *testing.T) {
	f := newGateFixture(t)
	a := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	held := &fakeHeld{}
	sp := f.p.arm("started.before-registry")
	go func() { _, _ = f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil }) }()
	sp.wait()
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	ap := f.p.arm("abort.before-write")
	sp.resume()
	ap.wait()
	f.clock.Advance(DefaultTClose)
	o := outcome(t, cc)
	if o.Kind != ClosedWithStarters || len(o.Starters) != 1 || o.Starters[0] != a.ID() || len(o.Aborting) != 1 {
		t.Fatalf("outcome %+v", o)
	}
	ap.resume()
}

// The hung-starter list for the crash-only exit.
func TestHungStarters(t *testing.T) {
	f := newGateFixture(t)
	tk := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	if len(f.g.HungStarters()) != 0 {
		t.Fatal("hung too early")
	}
	f.clock.Advance(DefaultTHung)
	if got := f.g.HungStarters(); len(got) != 1 || got[0] != tk.ID() {
		t.Fatalf("hung = %v", got)
	}
}

// Clock rollback does not reopen admission: the fence is the persisted state, never a time.
func TestClockRollbackDoesNotReopenAdmission(t *testing.T) {
	f := newGateFixture(t)
	f.g.Seal()
	f.clock.mu.Lock()
	f.clock.now = f.clock.now.Add(-48 * time.Hour)
	f.clock.mu.Unlock()
	if _, err := f.g.Admit(Spec{Kind: KindE1}); !errors.Is(err, ErrGateClosed) {
		t.Fatalf("Admit after rollback = %v", err)
	}
}

// A Release that fails after Started killed the child: the ticket must not stay STARTED.
func TestAReleaseErrorAfterStartedDoesNotLeaveTheTicketStarted(t *testing.T) {
	f := newGateFixture(t)
	held := &releaseFails{}
	tk, err := f.g.StartHeld(Spec{}, func() (Held, error) { return held, nil })
	if err == nil || !held.killed.Load() {
		t.Fatalf("err %v killed %v", err, held.killed.Load())
	}
	if got := f.g.State(tk); got != Aborted {
		t.Fatalf("state %v, want ABORTED (the child is dead)", got)
	}
	if _, ok := f.reg.abortedApproval(tk.ID()); !ok {
		t.Fatal("the abort was not recorded")
	}
}

type releaseFails struct{ fakeHeld }

func (h *releaseFails) Release() error { return errInjected }
