package doomsday

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// A target is read only where the text names exactly one plain count of things; anything else is no counter.
func TestCriterionTargetIsConservative(t *testing.T) {
	yes := map[string]int{
		"50 applications sent":    50,
		"Send 10 emails to leads": 10,
		"at least 1,000 signups":  1000,
		"Apply to 25 jobs.":       25,
		"Ship 3 features":         3,
	}
	for spec, want := range yes {
		if got, ok := CriterionTarget(spec); !ok || got != want {
			t.Errorf("%q: got %d %v, want %d", spec, got, ok, want)
		}
	}
	for _, spec := range []string{
		"", "go test ./...", "send fifty emails", "improve conversion by 20%", "send 10 emails to 50 leads", "release v2 notes",
		"revenue of $500", "meeting at 10:30 daily", "send 10", "ship by 2026-12-01 a build", "ratio 1.5 per page", "0 failures",
		"send 5000000000 emails", "3rd place finish", "10-20 applications", "step 2/3 done",
	} {
		if got, ok := CriterionTarget(spec); ok {
			t.Errorf("%q: read a target %d, want none", spec, got)
		}
	}
}

func TestStatusViewShowsTheJudgeReportedCountAgainstTheTarget(t *testing.T) {
	e := newFenv(t, nil)
	e.runs.set(func(r *StrategyRun) {
		r.Criteria = []StrategyCriterion{{ID: store.CriterionID(0), Text: "50 applications sent"}}
	})
	r := e.reader().(strategyReader).withRuns(e.runs)

	// No judgment yet: the target is shown, the count is not guessed.
	st, err := r.ReadStatus(bg, 0)
	if err != nil || len(st.Progress) != 1 || st.Progress[0].Target != 50 || st.Progress[0].Count != nil {
		t.Fatalf("%+v %v", st.Progress, err)
	}

	// A judgment that reports no count keeps it unknown (never zero).
	e.tickAfter(t, 31*time.Minute)
	if st, _ := r.ReadStatus(bg, 0); len(st.Progress) != 1 || st.Progress[0].Count != nil {
		t.Fatalf("%+v", st.Progress)
	}

	n := 12
	e.model.setCount(&n)
	e.tickAfter(t, 31*time.Minute)
	st, err = r.ReadStatus(bg, 0)
	if err != nil || len(st.Progress) != 1 || st.Progress[0].Count == nil || *st.Progress[0].Count != 12 || st.Progress[0].Target != 50 {
		t.Fatalf("%+v %v", st.Progress, err)
	}
	var b bytes.Buffer
	WriteProgress(&b, st.Progress)
	if !strings.Contains(b.String(), "12 / 50") || !strings.Contains(b.String(), "judge-reported") {
		t.Fatalf("%q", b.String())
	}
}

func TestStatusViewHasNoCounterForACriterionWithoutATarget(t *testing.T) {
	e := newFenv(t, nil) // its criterion is "go test ./..."
	n := 4
	e.model.setCount(&n)
	e.tickAfter(t, 31*time.Minute)
	st, err := e.reader().(strategyReader).withRuns(e.runs).ReadStatus(bg, 0)
	if err != nil || len(st.Progress) != 0 {
		t.Fatalf("%+v %v", st.Progress, err)
	}
	var b bytes.Buffer
	WriteProgress(&b, st.Progress)
	if b.Len() != 0 {
		t.Fatalf("%q", b.String())
	}
}

// `doomsday status` shows the counter of an open run, shows nothing when the reading fails, and never fails because of it.
func TestStatusCommandPrintsTheProgressLine(t *testing.T) {
	n := 12
	fr := &fakeStrategyReader{status: StrategyStatusView{Progress: []CriterionCountView{{ID: "c1", Text: "50 applications sent", Target: 50, Count: &n}}}}
	r := newRig()
	r.deps.ReadStrategyReader = func() StrategyReader { return fr }
	out, err := run(t, r.deps, "", "status")
	if err != nil || !strings.Contains(out, "progress: 12 / 50 (judge-reported, not verified): 50 applications sent") {
		t.Fatalf("%v %q", err, out)
	}
	fr.failWith = errors.New("boom")
	fr.status = StrategyStatusView{}
	out, err = run(t, r.deps, "", "status")
	if err != nil || strings.Contains(out, "progress") || !strings.Contains(out, "running") {
		t.Fatalf("%v %q", err, out)
	}
}
