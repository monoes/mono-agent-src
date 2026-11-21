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

// A unit mechanism that states its own limit (the session unit) is quoted by the report; every other best-effort end, and any note that is
// not exactly a mechanism's declared limit, keeps the process-group wording the CLI, the docs and the transcript tests pin.
func TestStatedLimitIsTheUnitsOwnWhenItDeclaresOne(t *testing.T) {
	session := Report{Generation: 7, Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitBestEffort, Note: SessionBestEffortNote}}
	if got := session.StatedLimit(); got != SessionBestEffortNote {
		t.Fatalf("session limit %q", got)
	}
	last := func(r Report) string { l := r.Lines(); return l[len(l)-1] }
	if got, want := last(session), "generation 7 stopped ("+SessionBestEffortNote+")"; got != want {
		t.Fatalf("report line %q, want %q", got, want)
	}
	for name, note := range map[string]string{"an empty unit": "no process of the run's sessions exists; " + SessionBestEffortNote, "joined with another unit's note": "another unit; " + SessionBestEffortNote + "; the witness and run directory disagree"} {
		r := Report{Generation: 7, Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitBestEffort, Note: note}}
		if got := r.StatedLimit(); got != SessionBestEffortNote {
			t.Errorf("%s: limit %q", name, got)
		}
	}
	for name, note := range map[string]string{"no note": "", "a pgroup note": "no process of the group exists", "text that is not a declared limit": "best effort: anything"} {
		r := Report{Generation: 7, Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitBestEffort, Note: note}}
		if got := r.StatedLimit(); got != BestEffortNote {
			t.Errorf("%s: limit %q", name, got)
		}
		if got, want := last(r), "generation 7 stopped ("+BestEffortNote+")"; got != want {
			t.Errorf("%s: report line %q", name, got)
		}
	}
	// the limit is only ever stated for a best-effort end
	if got := (Report{Final: StateQuiescent, Unit: UnitOutcome{Kind: UnitEnded, Note: SessionBestEffortNote}}).StatedLimit(); got != BestEffortNote {
		t.Fatalf("a proven end carries a limit: %q", got)
	}
}
