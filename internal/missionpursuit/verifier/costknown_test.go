package verifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// Revert proof: drop the CostKnown fix in Run and a check that never reached the reviewer is "unknown", which the host's meter
// charges at the cap: $0.50 for a check that cost nothing.
func TestACheckThatNeverReachedTheReviewerCostsAKnownZero(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*rig)
		cs   []Criterion
		want Outcome
	}{
		{"failed", func(*rig) {}, []Criterion{fileExists("c1"), crit("c2", "file", "missing.txt", "exists")}, OutcomeFailed},
		{"error", func(*rig) {}, []Criterion{crit("c1", "file", "a.txt", "nonsense")}, OutcomeError},
		{"cannot-check", func(*rig) {}, []Criterion{crit("c1", "rubric", "is it good", "")}, OutcomeCannotCheck},
		{"no budget", func(r *rig) { r.meter.err = errors.New("over") }, []Criterion{fileExists("c1")}, OutcomeBlocked},
		{"stale", func(r *rig) {
			r.cfg.afterCriteria = func() { _ = os.WriteFile(filepath.Join(r.root, "a.txt"), []byte("alpha!"), 0o644) }
		}, []Criterion{fileExists("c1")}, OutcomeStale},
	} {
		r := newRig(t, true)
		c.mut(r)
		got := r.run(c.cs...)
		if got.Outcome != c.want || !got.CostKnown || got.CostMicro != 0 || r.rev.calls != 0 {
			t.Errorf("%s: %s", c.name, dump(got))
		}
	}
}

func TestAnAbandonedCheckAndAnUnreportedReviewerCostStayUnknown(t *testing.T) {
	r := newRig(t, true)
	k := r.checker()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, _ := k.Run(ctx, r.req(fileExists("c1")))
	if got.Outcome != OutcomeAbandoned || got.CostKnown {
		t.Errorf("an abandoned check is charged by the host at the cap, so it must not claim a known zero: %s", dump(got))
	}
	r = newRig(t, true) // the reviewer ran and reported no cost: the cap, not known
	got = r.run(fileExists("c1"))
	if got.Outcome != OutcomePassedMachine || got.CostKnown || got.CostMicro != 500000 {
		t.Errorf("an unreported reviewer cost: %s", dump(got))
	}
}

// Revert proof: send the excerpts of an unconfined run to the reviewer and this goes red.
func TestOutputTextGoesToTheReviewerOnlyForConfinedRuns(t *testing.T) {
	mk := func(containment string) *Result {
		return &Result{Containment: containment, Criteria: []CriterionResult{
			{ID: "c1", Hash: h64("c1"), Kind: "command", Result: ResPass, StdoutDigest: "sd", StderrDigest: "ed", Stdout: "SECRET-OUT", Stderr: "SECRET-ERR", Detail: "SECRET-DETAIL", RunsSubjectCode: true},
			{ID: "c2", Hash: h64("c2"), Kind: "file", Result: ResPass, Detail: "file detail"},
		}}
	}
	has := func(ev []finish.Evidence, s string) bool {
		for _, e := range ev {
			if strings.Contains(e.StdoutExcerpt+e.StderrExcerpt+e.Detail, s) {
				return true
			}
		}
		return false
	}
	confined := evidenceOf(mk(ContainmentConfinedPrefix + "systemd-run + process-group"))
	if !has(confined, "SECRET-OUT") || !has(confined, "SECRET-ERR") || !has(confined, "SECRET-DETAIL") {
		t.Fatalf("a confined run's text did not reach the reviewer: %+v", confined)
	}
	un := evidenceOf(mk(ContainmentUnconfined))
	if has(un, "SECRET") || has(un, "file detail") {
		t.Fatalf("an unconfined run's text reached the reviewer: %+v", un)
	}
	if un[0].StdoutDigest != "sd" || un[0].Result != ResPass || len(un) != 2 {
		t.Errorf("digests, results and exit codes must still go: %+v", un)
	}
}
