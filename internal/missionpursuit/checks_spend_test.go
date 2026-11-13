package missionpursuit

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

func (w checkWorld) finish(t *testing.T, id int64, o verifier.Outcome, reason verifier.Reason, micro int64, known bool, verdict string) {
	t.Helper()
	res := w.result(o, verdict)
	res.Reason, res.CostMicro, res.CostKnown = reason, micro, known
	if err := w.hc.Finish(bg, id, res); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSpendCountsARunningCheckAtTheCapAndAnUnknownCostAtTheCap(t *testing.T) {
	w := newCheckWorld(t)
	run := w.begin(t)
	got, err := CheckSpendAfter(bg, w.db, w.session, 0)
	if err != nil || got.Micro != CheckCapMicro || got.Count != 1 {
		t.Fatalf("a running check: %+v %v", got, err)
	}
	w.finish(t, run, verifier.OutcomeFailed, verifier.ReasonCriteriaFailed, 0, true, "") // no reviewer call: known, nothing spent
	if got, _ := CheckSpendAfter(bg, w.db, w.session, 0); got.Micro != 0 || got.Count != 1 {
		t.Errorf("a failed check with a known zero cost: %+v", got)
	}
	two := w.begin(t)
	w.finish(t, two, verifier.OutcomePassedMachine, "", 1500, true, "ok")
	if got, _ := CheckSpendAfter(bg, w.db, w.session, 0); got.Micro != 1500 || got.Count != 2 {
		t.Errorf("a known cost is charged as it is: %+v", got)
	}
	three := w.begin(t)
	w.finish(t, three, verifier.OutcomeBlocked, verifier.ReasonReviewerUnavailable, CheckCapMicro, false, "")
	four := w.begin(t)
	w.finish(t, four, verifier.OutcomeAbandoned, "", 0, false, "")
	five := w.begin(t) // done, cost 0, not known: stored as 0 but never free
	w.finish(t, five, verifier.OutcomeBlocked, verifier.ReasonReviewerUnavailable, 0, false, "")
	got, _ = CheckSpendAfter(bg, w.db, w.session, 0)
	if want := int64(1500) + 3*CheckCapMicro; got.Micro != want || got.Count != 5 {
		t.Errorf("an unknown cost and an abandoned check count at the cap: %+v, want %d", got, want)
	}
	// after the person's action only the later checks count
	if got, _ := CheckSpendAfter(bg, w.db, w.session, three); got.Count != 2 || got.Micro != 2*CheckCapMicro {
		t.Errorf("after id %d: %+v", three, got)
	}
}

func TestNextCheckRoundComesFromTheRowsAndSurvivesAbandonedOnes(t *testing.T) {
	w := newCheckWorld(t)
	if n, err := NextCheckRound(bg, w.db, w.session); err != nil || n != 1 {
		t.Fatalf("first round = %d %v", n, err)
	}
	for i := 0; i < 3; i++ {
		w.finish(t, w.begin(t), verifier.OutcomeFailed, verifier.ReasonCriteriaFailed, 0, true, "")
	}
	run := w.begin(t) // round 4, running: a host restart
	if n, err := w.hc.AbandonRunning(bg, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	_ = run
	if n, _ := NextCheckRound(bg, w.db, w.session); n != 5 {
		t.Errorf("round after a restart = %d, want 5 (rounds 1 to 4 exist, one abandoned)", n)
	}
}

func TestAbandonRunningSparesChecksStartedAfterTheHostStarted(t *testing.T) {
	w := newCheckWorld(t)
	run := w.begin(t)
	if _, err := w.hc.AbandonRunning(bg, time.Time{}); err == nil {
		t.Error("a sweep with no start time was accepted")
	}
	if n, err := w.hc.AbandonRunning(bg, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("a host that started an hour ago swept %d, %v: a check that started after it is live", n, err)
	}
	if c := w.read(t, run); c.State != CheckRunning {
		t.Errorf("the live check was abandoned: %+v", c)
	}
	if n, _ := w.hc.AbandonRunning(bg, time.Now().Add(time.Minute)); n != 1 {
		t.Errorf("a host that started after the check did not sweep it: %d", n)
	}
}

func TestHostChecksSinceCountsTheWindowAndUnreadableTimes(t *testing.T) {
	w := newCheckWorld(t)
	for i := 0; i < 3; i++ {
		w.finish(t, w.begin(t), verifier.OutcomeFailed, verifier.ReasonCriteriaFailed, 0, true, "")
	}
	if n, err := HostChecksSince(bg, w.db, time.Now().Add(-time.Hour)); err != nil || n != 3 {
		t.Fatalf("checks of the last hour = %d %v", n, err)
	}
	if n, _ := HostChecksSince(bg, w.db, time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("checks after an hour from now = %d", n)
	}
}

func TestReadCheckFactsAwaitingPersonSinceVerdictAndBadReviewerRuns(t *testing.T) {
	w := newCheckWorld(t)
	for i := 0; i < 2; i++ {
		w.finish(t, w.begin(t), verifier.OutcomeBlocked, verifier.ReasonReviewerUnavailable, CheckCapMicro, false, "")
	}
	w.finish(t, w.begin(t), verifier.OutcomeBlocked, verifier.ReasonReviewerBlocked, 1500, true, "block") // a verdict is not "unusable"
	w.finish(t, w.begin(t), verifier.OutcomeBlocked, verifier.ReasonReviewerInvalid, CheckCapMicro, false, "")
	f, err := ReadCheckFacts(bg, w.db, w.session, 0)
	if err != nil || f.BadReviewer != 1 || f.SinceVerdict != 4 || f.Running || f.AwaitingPerson {
		t.Fatalf("facts = %+v %v", f, err)
	}
	for i := 0; i < 2; i++ {
		w.finish(t, w.begin(t), verifier.OutcomeBlocked, verifier.ReasonReviewerPinChanged, CheckCapMicro, false, "")
	}
	f, _ = ReadCheckFacts(bg, w.db, w.session, 0)
	if f.BadReviewer != 3 {
		t.Errorf("bad reviewer runs = %d, want 3 in a row", f.BadReviewer)
	}
	head, _ := CheckHead(bg, w.db, w.session)
	if f, _ = ReadCheckFacts(bg, w.db, w.session, head); f.BadReviewer != 3 {
		t.Errorf("the count runs across away windows (the host lifts the stop when the person acts), got %d", f.BadReviewer)
	}
	w.finish(t, w.begin(t), verifier.OutcomePassedMachine, "", 1500, true, "ok")
	f, _ = ReadCheckFacts(bg, w.db, w.session, 0)
	if !f.AwaitingPerson || f.LatestOutcome != OutcomePassedMachine || f.BadReviewer != 0 {
		t.Errorf("a passed check waits for the person: %+v", f)
	}
	run := w.begin(t)
	if f, _ = ReadCheckFacts(bg, w.db, w.session, 0); !f.Running || f.LatestID != run || f.AwaitingPerson {
		t.Errorf("running: %+v", f)
	}
}

func TestTheReviewerUnusableReasonsAreTheLibrarysWords(t *testing.T) {
	for _, r := range []verifier.Reason{verifier.ReasonReviewerUnavailable, verifier.ReasonReviewerInvalid, verifier.ReasonReviewerPinChanged} {
		if !reviewerUnusable(string(r)) {
			t.Errorf("%s is not counted as an unusable reviewer", r)
		}
	}
	if reviewerUnusable(string(verifier.ReasonReviewerBlocked)) || string(verifier.OutcomeBlocked) != "blocked" {
		t.Error("a reviewer's verdict counted as an unusable reviewer, or the outcome word moved")
	}
}

// A refused confinement names the self test that failed, in the containment of the check row.
func TestACheckRefusedForItsConfinementKeepsWhichTestFailed(t *testing.T) {
	w := newCheckWorld(t)
	id := w.begin(t)
	res := w.result(verifier.OutcomeCannotCheck, "")
	res.Reason, res.ConfinementDetail, res.CostMicro, res.CostKnown = verifier.ReasonNoConfinement, "C4 secrets by path: the host's .ssh folder was visible", 0, true
	if err := w.hc.Finish(bg, id, res); err != nil {
		t.Fatal(err)
	}
	if c := w.read(t, id); c.Reason != "no-confinement" || !strings.Contains(c.Containment, "C4 secrets by path") {
		t.Errorf("reason %q containment %q", c.Reason, c.Containment)
	}
}
