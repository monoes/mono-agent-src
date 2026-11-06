package stop

import (
	"strings"
	"testing"
)

// A best-effort containment (a Windows Job Object, where a process that left the group is invisible to the unit) must
// never produce the bare word "stopped": Stopped() stays false for it and the report names the limit.
func TestBestEffortUnitIsNeverBareStopped(t *testing.T) {
	s, st, en, _ := newStopper(t, nil)
	en.out = UnitOutcome{Kind: UnitBestEffort}
	rep := s.Stop(bg, 7, "ch1", "operator")
	if rep.Final != StateQuiescent {
		t.Fatalf("a best-effort end with nothing else wrong settles the run: %s (log %v)", joined(rep), st.log)
	}
	if rep.Stopped() {
		t.Fatalf("Stopped() must be false for a best-effort unit: %s", joined(rep))
	}
	if !rep.StoppedBestEffort() || rep.ExitCode() != 0 {
		t.Fatalf("the best-effort stop is its own answer: %s", joined(rep))
	}
	out := joined(rep)
	const want = "stopped (best effort: processes that left the group are not covered)"
	if !strings.Contains(out, want) {
		t.Fatalf("report lacks %q: %s", want, out)
	}
	for _, l := range rep.Lines() {
		if strings.Contains(l, " stopped") && !strings.Contains(l, want) && !strings.Contains(l, "NOT stopped") {
			t.Fatalf("a bare stopped line: %q", l)
		}
	}
}

func TestBestEffortNeverHidesAFailure(t *testing.T) {
	r := Report{Generation: 1, Final: StateCleanupPending, Unit: UnitOutcome{Kind: UnitBestEffort}}
	if r.Stopped() || r.StoppedBestEffort() || r.ExitCode() == 0 {
		t.Fatal("a run that is not quiescent is not stopped, best effort or not")
	}
	r = Report{Generation: 1, Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitEnded}}
	if !r.Stopped() || r.StoppedBestEffort() {
		t.Fatal("a proven end is Stopped and not best effort")
	}
	r = Report{Generation: 1, Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitBestEffort}, Steps: []Step{{Name: StepMarker, Err: errInjected}}}
	if r.StoppedBestEffort() {
		t.Fatal("an unrecorded step prevents the best-effort stop too")
	}
}

func TestMergeOutcomeRanksBestEffortBetweenEndedAndUnknown(t *testing.T) {
	if got := mergeOutcome(UnitOutcome{Kind: UnitEnded}, UnitOutcome{Kind: UnitBestEffort}).Kind; got != UnitBestEffort {
		t.Fatalf("ended + best effort = %d", got)
	}
	if got := mergeOutcome(UnitOutcome{Kind: UnitBestEffort}, UnitOutcome{Kind: UnitEndedWithUnknown}).Kind; got != UnitEndedWithUnknown {
		t.Fatalf("best effort + unknown = %d", got)
	}
	if got := mergeOutcome(UnitOutcome{Kind: UnitBestEffort}, UnitOutcome{Kind: UnitEnded}).Kind; got != UnitBestEffort {
		t.Fatalf("best effort + ended = %d", got)
	}
}
