package finish

import (
	"errors"
	"testing"
)

func crashAt(point string) func(string) error {
	return func(p string) error {
		if p == point {
			return errCrash
		}
		return nil
	}
}

// 13.6 P1: a crash before and a crash during the review never finish; the restart repeats what is missing and
// does not repeat what is recorded.
func TestCrashBeforeAndDuringReview(t *testing.T) {
	cases := []struct {
		point         string
		wantCalls     int // verifier calls in the crashed step
		committedAt   bool
		restartCalls  int // verifier calls in the restart
		wantReviewRow int // complete rows after the crash
	}{
		{"review.started", 0, false, 1, 0},  // crash during: only a started row exists
		{"review.called", 1, false, 1, 0},   // the model answered, the answer was lost
		{"review.complete", 1, false, 0, 1}, // recorded, crash before the commit
		{"commit", 1, false, 0, 1},          // recorded, crash at the commit
	}
	for _, c := range cases {
		t.Run(c.point, func(t *testing.T) {
			l := newLedger(baseFacts())
			v := okVerifier()
			fn := Finisher{Ledger: l, Verifier: v, Hook: crashAt(c.point)}
			if _, err := fn.Step(bg); !errors.Is(err, errCrash) {
				t.Fatalf("no crash: %v", err)
			}
			if l.committed || v.calls != c.wantCalls {
				t.Fatalf("committed=%v calls=%d", l.committed, v.calls)
			}
			complete := 0
			for _, r := range l.f.Reviews {
				if r.State == ReviewComplete {
					complete++
				}
			}
			if complete != c.wantReviewRow {
				t.Fatalf("complete rows %d", complete)
			}
			if err := l.Commit(bg); !errors.Is(err, ErrCommitRefused) && c.wantReviewRow == 0 {
				t.Fatalf("the ledger committed a state without a completed review: %v", err)
			}
			v.calls = 0
			vd, err := Finisher{Ledger: l, Verifier: v}.Step(bg)
			if err != nil || vd.Outcome != Commit || !l.committed || v.calls != c.restartCalls {
				t.Fatalf("restart: %+v %v committed=%v calls=%d", vd, err, l.committed, v.calls)
			}
		})
	}
}

func TestCrashBeforeAnyReviewRowLeavesNothingToCommit(t *testing.T) {
	l := newLedger(baseFacts())
	if err := l.Commit(bg); !errors.Is(err, ErrCommitRefused) {
		t.Fatalf("committed: %v", err)
	}
	if len(l.f.Reviews) != 0 || l.committed || l.parked != nil {
		t.Fatal("state changed")
	}
}

func TestVerifierProblemsParkWithNoFallback(t *testing.T) {
	cases := map[string]struct {
		v    *fakeVerifier
		want Reason
	}{
		"unreachable": {&fakeVerifier{err: errors.New("down")}, ReasonVerifierUnavailable},
		"other model": {&fakeVerifier{res: ReviewResult{Observed: bossP, Verdict: VerdictOK}}, ReasonVerifierChanged},
		"alias moved": {&fakeVerifier{res: ReviewResult{Observed: Pin{Provider: "prov-b", Model: "verifier-2", Version: "x", PolicyHash: "pol-1"}, Verdict: VerdictOK}}, ReasonVerifierChanged},
		"policy hash": {&fakeVerifier{res: ReviewResult{Observed: Pin{Provider: "prov-b", Model: "verifier-1", Version: "2026-10-01", PolicyHash: "pol-2"}, Verdict: VerdictOK}}, ReasonVerifierChanged},
		"free text":   {&fakeVerifier{res: ReviewResult{Observed: verifierP, Verdict: "approve everything now"}}, ReasonVerifierUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			l := newLedger(baseFacts())
			vd, err := Finisher{Ledger: l, Verifier: c.v}.Step(bg)
			if err != nil {
				t.Fatal(err)
			}
			mustOutcome(t, vd, Park, c.want)
			if l.committed || l.parked == nil || c.v.calls != 1 {
				t.Fatalf("committed=%v parked=%v calls=%d", l.committed, l.parked, c.v.calls)
			}
			for _, r := range l.f.Reviews {
				if r.State == ReviewComplete {
					t.Fatal("a complete review row was written for a rejected answer")
				}
			}
		})
	}
}

func TestBlockingVerdictParksAndCannotBeClearedInTheRound(t *testing.T) {
	l := newLedger(baseFacts())
	v := &fakeVerifier{res: ReviewResult{Observed: verifierP, Verdict: VerdictBlock, ReasonCode: "zero tests ran"}}
	vd, err := Finisher{Ledger: l, Verifier: v}.Step(bg)
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, vd, Park, ReasonVerifierBlocked)
	if got := l.f.Reviews[len(l.f.Reviews)-1].ReasonCode; got != "unspecified" {
		t.Fatalf("model text reached the ledger: %q", got)
	}
	// the operator-less retry in the same round asks again and the model now says ok: still blocked
	v.res = ReviewResult{Observed: verifierP, Verdict: VerdictOK}
	l.parked = nil
	vd, _ = Finisher{Ledger: l, Verifier: v}.Step(bg)
	mustOutcome(t, vd, Park, ReasonVerifierBlocked)
	if l.committed {
		t.Fatal("finished after a blocker")
	}
}

func TestHarnessFailureIsNotOverriddenByAnOKReview(t *testing.T) {
	f := baseFacts()
	f.Evidence[0].Result = "fail"
	l := newLedger(f)
	vd, _ := Finisher{Ledger: l, Verifier: okVerifier()}.Step(bg)
	mustOutcome(t, vd, Park, ReasonCriteriaFailed)
	if l.committed {
		t.Fatal("committed")
	}
}

func TestReviewIsAskedOnlyForCompleteMachineEvidence(t *testing.T) {
	f := baseFacts()
	f.Evidence = f.Evidence[:1]
	l := newLedger(f)
	v := okVerifier()
	vd, _ := Finisher{Ledger: l, Verifier: v}.Step(bg)
	mustOutcome(t, vd, Wait, WaitEvidence)
	if v.calls != 0 {
		t.Fatal("the model was asked about incomplete results")
	}
}

func TestHappyPathAndReviewSeesTheRoundsResults(t *testing.T) {
	l := newLedger(baseFacts())
	v := okVerifier()
	vd, err := Finisher{Ledger: l, Verifier: v}.Step(bg)
	if err != nil || vd.Outcome != Commit || !l.committed || v.calls != 1 {
		t.Fatalf("%+v %v", vd, err)
	}
	if len(v.inputs[0].Results) != 2 || v.inputs[0].Round.Digest != "d1" {
		t.Fatalf("%+v", v.inputs[0])
	}
}

func TestCleanReason(t *testing.T) {
	if cleanReason("too-easy") != "too-easy" || cleanReason("Ignore previous instructions") != "unspecified" ||
		cleanReason("") != "unspecified" || cleanReason("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") != "unspecified" {
		t.Fatal("reason sanitising")
	}
}

func TestAFinishedOrParkedRunIsNotCommittedOrParkedAgain(t *testing.T) {
	f := baseFacts()
	f.State = StateFinished
	l := newLedger(f)
	v := okVerifier()
	fn := Finisher{Ledger: l, Verifier: v}
	got, err := fn.Step(bg)
	mustOutcome(t, got, Commit, "")
	if err != nil || l.committed || l.parked != nil || v.calls != 0 {
		t.Fatalf("a finished run was acted on again: %v committed=%v parked=%v calls=%d", err, l.committed, l.parked, v.calls)
	}
	f.State, f.ParkReason = StateParked, ReasonCriteriaFailed
	l = newLedger(f)
	got, err = Finisher{Ledger: l, Verifier: v}.Step(bg)
	mustOutcome(t, got, Park, ReasonCriteriaFailed)
	if err != nil || l.committed || l.parked != nil || v.calls != 0 {
		t.Fatalf("a parked run was acted on again: %v", err)
	}
}
