package stop

import (
	"errors"
	"testing"
)

func startOp(t *testing.T, f *gateFixture, steps ...string) *Ticket {
	t.Helper()
	tk, err := f.g.StartHeld(Spec{Op: "merge", Steps: steps}, func() (Held, error) { return &fakeHeld{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// startStep admits, begins and starts a continuation step.
func startStep(t *testing.T, f *gateFixture, step string) *Ticket {
	t.Helper()
	tk, err := f.g.AdmitContinuation("merge", step)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.g.BeginStart(tk); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Started(tk, "h"); err != nil {
		t.Fatal(err)
	}
	return tk
}

// T35: a begun merge paused between step 1 and 2; Close does not return BARRIER; step 2 runs through its
// continuation ticket; the drain resolves when the operation is FINISHED.
func TestT35BegunOperationFinishesAfterClose(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	notYet(t, cc, "the operation is CONTINUING")
	f.g.EndWork(first, true)
	notYet(t, cc, "step b has not run")
	b := startStep(t, f, "b")
	f.g.EndWork(b, true)
	if o := outcome(t, cc); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
}

// T36 + the 13.1 P1: Close is called while another ticket is STARTING; the operation proceeds to its next step
// before that ticket drains. The continuation starter is part of the live set, so Close still waits and no step
// is created after a BARRIER that was returned.
func TestT36ContinuationStarterIsPartOfTheLiveSet(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	other := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	f.g.EndWork(first, true)
	f.g.Abort(other)
	notYet(t, cc, "operation still CONTINUING after the other ticket drained")
	b, err := f.g.AdmitContinuation("merge", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.g.BeginStart(b); err != nil {
		t.Fatal(err)
	}
	notYet(t, cc, "continuation ticket b is STARTING")
	if err := f.g.Started(b, "h"); err != nil {
		t.Fatal(err)
	}
	f.g.EndWork(b, true)
	if o := outcome(t, cc); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
}

// T43 (the recorded P1 of 13.1): Close while A is STARTING; the operation then admits and starts continuation
// ticket B; at the deadline the outcome lists A and B, not only the tickets of a frozen snapshot.
func TestT43DeadlineListsStartersCreatedAfterClose(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	a := mustAdmitStart(t, f.g, Spec{Kind: KindE1})
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	f.g.EndWork(first, true)
	b, err := f.g.AdmitContinuation("merge", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.g.BeginStart(b); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(DefaultTClose)
	o := outcome(t, cc)
	if o.Kind != ClosedWithStarters || len(o.Starters) != 2 || o.Starters[0] != a.ID() || o.Starters[1] != b.ID() {
		t.Fatalf("outcome %+v, want starters %d and %d", o, a.ID(), b.ID())
	}
	// T44: re-evaluation on every resolution; BARRIER only when the live set is empty.
	f.g.Abort(a)
	if k := f.g.Outcome().Kind; k != ClosedWithStarters {
		t.Fatalf("after A resolves: %v", k)
	}
	if err := f.g.Started(b, "h"); err != nil {
		t.Fatal(err)
	}
	if k := f.g.Outcome().Kind; k != ClosedWithOperation {
		t.Fatalf("after B started: %v, want CLOSED-WITH-OPERATION", k)
	}
	f.g.EndWork(b, true)
	if k := f.g.Outcome().Kind; k != Barrier {
		t.Fatalf("after the operation finished: %v", k)
	}
}

// Deadline-after-admission (13.1 P1): a hung continuation starter admitted after Close is reported by
// HungStarters so the host's crash-only exit covers it too.
func TestDeadlineAfterAdmissionHungContinuationStarter(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	f.g.EndWork(first, true)
	b, _ := f.g.AdmitContinuation("merge", "b")
	if err := f.g.BeginStart(b); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(DefaultTClose)
	if o := outcome(t, cc); o.Kind != ClosedWithStarters {
		t.Fatalf("outcome %v", o.Kind)
	}
	f.clock.Advance(DefaultTHung)
	if h := f.g.HungStarters(); len(h) != 1 || h[0] != b.ID() {
		t.Fatalf("hung = %v: the continuation starter must be tracked until it terminates", h)
	}
}

// T37: a failing step makes the operation INCOMPLETE; the aborted step leaves CLOSED-WITH-UNKNOWN.
func TestT37FailedStepIsIncomplete(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	f.g.EndWork(first, false)
	if _, err := f.g.AdmitContinuation("merge", "b"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("a step of an INCOMPLETE operation was admitted: %v", err)
	}
	if o := f.g.Close(); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
}

// T38: T_merge expiry makes the operation INCOMPLETE and withdraws the remaining steps.
func TestT38MergeExpiryWithdrawsSteps(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	f.g.EndWork(first, true)
	f.clock.Advance(DefaultTMerge)
	if o := outcome(t, cc); o.Kind == ClosedWithStarters {
		t.Fatalf("outcome %v", o.Kind)
	}
	if _, err := f.g.AdmitContinuation("merge", "b"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("step after T_merge admitted: %v", err)
	}
}

// T39: a new operation, a step out of order, a step outside the list, a step after T_merge: each refused.
func TestT39AdmitContinuationRefusals(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b", "c")
	f.g.Seal()
	for name, c := range map[string][2]string{
		"new operation":    {"other", "b"},
		"out of order":     {"merge", "c"},
		"outside list":     {"merge", "zzz"},
		"first step again": {"merge", "a"},
	} {
		if _, err := f.g.AdmitContinuation(c[0], c[1]); !errors.Is(err, ErrContinuation) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := f.g.AdmitContinuation("merge", "b"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("a step before its predecessor ended was admitted: %v", err)
	}
	f.g.EndWork(first, true)
	if _, err := f.g.AdmitContinuation("merge", "b"); err != nil {
		t.Fatal(err)
	}
}

// T41: an ADMITTED continuation ticket paused before BeginStart while the operation ends is FENCED at the
// terminal transition; no process is created, also after BARRIER. For each reachable terminal cause.
func TestT41WithdrawalOnEveryTerminalTransition(t *testing.T) {
	causes := map[string]func(f *gateFixture, first *Ticket){
		"failing step": func(f *gateFixture, first *Ticket) { f.g.EndWork(first, false) },
		"expiry":       func(f *gateFixture, first *Ticket) { f.clock.Advance(DefaultTMerge) },
	}
	for name, end := range causes {
		t.Run(name, func(t *testing.T) {
			f := newGateFixture(t)
			first := startOp(t, f, "a", "b")
			f.g.Seal()
			f.g.EndWork(first, true)
			b, err := f.g.AdmitContinuation("merge", "b")
			if err != nil {
				t.Fatal(err)
			}
			pp := f.p.arm("beginstart.before")
			errc := make(chan error, 1)
			go func() { errc <- f.g.BeginStart(b) }()
			pp.wait()
			end(f, first)
			if st := f.g.State(b); st != Fenced {
				t.Fatalf("state %v at the terminal transition, want FENCED (withdrawn)", st)
			}
			if o := f.g.Close(); o.Kind != Barrier {
				t.Fatalf("outcome %v", o.Kind)
			}
			pp.resume()
			if err := <-errc; err == nil {
				t.Fatal("BeginStart succeeded after the operation left CONTINUING")
			}
			if st := f.g.State(b); st != Fenced {
				t.Fatalf("state %v, want FENCED", st)
			}
		})
	}
}

// An aborted or unknown step makes its operation INCOMPLETE (note 13.1 section 4.1, leaving CONTINUING).
func TestT37AbortedStepMakesOperationIncomplete(t *testing.T) {
	f := newGateFixture(t)
	f.reg.failStarted.Store(true)
	_, err := f.g.StartHeld(Spec{Op: "merge", Steps: []string{"a", "b"}}, func() (Held, error) { return &fakeHeld{}, nil })
	if !errors.Is(err, ErrRegistryWrite) {
		t.Fatal(err)
	}
	// the first step never became STARTED: the operation was never registered
	if _, err := f.g.AdmitContinuation("merge", "b"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("%v", err)
	}
	// a continuation step whose Started write fails ends the operation
	f.reg.failStarted.Store(false)
	f2 := newGateFixture(t)
	first := startOp(t, f2, "a", "b", "c")
	f2.g.EndWork(first, true)
	b, _ := f2.g.AdmitContinuation("merge", "b")
	_ = f2.g.BeginStart(b)
	f2.reg.failStarted.Store(true)
	if err := f2.g.Started(b, "h"); !errors.Is(err, ErrRegistryWrite) {
		t.Fatal(err)
	}
	if _, err := f2.g.AdmitContinuation("merge", "c"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("operation survived an UNKNOWN step: %v", err)
	}
	if k := f2.g.Outcome().Kind; k != ClosedWithUnknown {
		t.Fatalf("outcome %v", k)
	}
}

// T42: AdmitContinuation twice returns one ticket; a second BeginStart refuses and changes nothing.
func TestT42SingleTicketPerStep(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	f.g.EndWork(first, true)
	b1, _ := f.g.AdmitContinuation("merge", "b")
	b2, err := f.g.AdmitContinuation("merge", "b")
	if err != nil || b1 != b2 {
		t.Fatalf("second AdmitContinuation: %v same=%v", err, b1 == b2)
	}
	if err := f.g.BeginStart(b1); err != nil {
		t.Fatal(err)
	}
	if err := f.g.BeginStart(b2); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("second BeginStart = %v", err)
	}
	if f.g.State(b1) != Starting {
		t.Fatalf("state %v", f.g.State(b1))
	}
}

// Q11: the tickets of a CONTINUING operation are listed so Stop can exclude their units from the kill; an
// unbegun operation has no continuation right at all.
func TestContinuingTicketsAndUnbegunOperation(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	if got := f.g.ContinuingTickets(); len(got) != 1 || got[0] != first.ID() {
		t.Fatalf("continuing = %v", got)
	}
	// An operation whose first step never reached STARTED is not registered: no continuation.
	if _, err := f.g.AdmitContinuation("unbegun", "b"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("unbegun: %v", err)
	}
}
