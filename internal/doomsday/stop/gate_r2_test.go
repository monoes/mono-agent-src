package stop

import (
	"errors"
	"testing"
)

func continuing(g *Gate) bool { return len(g.ContinuingTickets()) > 0 }

// R2 (note 13.3 section 7): the bound passing while a step runs does not end the operation, so Stop's deferral of
// the containment kill still covers the running command. No new step is admitted past the bound.
func TestR2AStepInFlightKeepsTheOperationOpenPastTMerge(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	f.g.Seal()
	f.clock.Advance(DefaultTMerge + 1)
	if !continuing(f.g) {
		t.Fatal("the operation ended at T_merge while its step was running: the kill deferral would end under it")
	}
	f.g.EndWork(first, true)
	if continuing(f.g) {
		t.Fatal("the operation stayed open after its running step ended past the bound")
	}
	if _, err := f.g.AdmitContinuation("merge", "b"); !errors.Is(err, ErrContinuation) {
		t.Fatalf("a step was admitted past the bound: %v", err)
	}
}

// A step that is still running at the hard bound is no longer protected: the documented outcome (INCOMPLETE).
func TestR2BTheHardBoundEndsAStepThatNeverEnds(t *testing.T) {
	f := newGateFixture(t)
	startOp(t, f, "a", "b")
	f.g.Seal()
	f.clock.Advance(DefaultTMerge + DefaultTMergeHard)
	if continuing(f.g) {
		t.Fatal("a step that never ends kept the operation open past the hard bound")
	}
}

// The last step ending successfully past the bound finishes the operation.
func TestR2CTheLastStepEndingPastTheBoundFinishesTheOperation(t *testing.T) {
	f := newGateFixture(t)
	first := startOp(t, f, "a", "b")
	f.g.Seal()
	f.g.EndWork(first, true)
	b := startStep(t, f, "b")
	f.clock.Advance(DefaultTMerge + 1)
	if !continuing(f.g) {
		t.Fatal("the operation ended at the bound while its last step runs")
	}
	f.g.EndWork(b, true)
	if continuing(f.g) {
		t.Fatal("a finished operation is still open")
	}
}

// A Close that waits on a running step past the bound must park until the step ends, not spin.
func TestR2DCloseWaitsForARunningStepPastTheBound(t *testing.T) {
	f := newGateFixture(t)
	f.g = NewGate(GateConfig{Generation: 7, Reg: f.reg, Clock: f.clock, Hook: f.p.hook, TClose: 3 * DefaultTMerge})
	first := startOp(t, f, "a")
	cc := closeAsync(f.g)
	waitSealed(t, f.g)
	f.clock.Advance(DefaultTMerge + 1)
	notYet(t, cc, "a step is running past the bound")
	f.g.EndWork(first, true)
	if o := outcome(t, cc); o.Kind != Barrier {
		t.Fatalf("outcome %v", o.Kind)
	}
}
