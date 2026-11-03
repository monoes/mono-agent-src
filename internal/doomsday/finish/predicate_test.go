package finish

import (
	"errors"
	"testing"
)

// 13.6 P1: machine evidence alone never commits.
func TestEvidenceWithoutReviewNeverCommits(t *testing.T) {
	f := baseFacts()
	mustOutcome(t, Evaluate(f), Wait, WaitReview)
	l := newLedger(f)
	if err := l.Commit(bg); !errors.Is(err, ErrCommitRefused) {
		t.Fatalf("the ledger committed without a review: %v", err)
	}
	f.Reviews = []Review{goodReview(f, 20)}
	mustOutcome(t, Evaluate(f), Commit, "")
}

// A review row that is not a complete, current-round, freeze- and digest-bound, clean review of these results
// under the pin never counts.
func TestReviewThatIsNotBoundNeverCounts(t *testing.T) {
	cases := map[string]func(r *Review, f *Facts){
		"started only":        func(r *Review, _ *Facts) { r.State = ReviewStarted },
		"other round":         func(r *Review, _ *Facts) { r.Round = 2 },
		"other freeze point":  func(r *Review, _ *Facts) { r.FreezeSeq = 99 },
		"other digest":        func(r *Review, _ *Facts) { r.Digest = "d0" },
		"other generation":    func(r *Review, _ *Facts) { r.Generation = 2 },
		"other charter":       func(r *Review, _ *Facts) { r.CharterHash = "ch-0" },
		"other results":       func(r *Review, _ *Facts) { r.ResultsDigest = "stale" },
		"before the evidence": func(r *Review, _ *Facts) { r.Seq = 11 },
		"empty verdict":       func(r *Review, _ *Facts) { r.Verdict = "" },
		"unknown verdict":     func(r *Review, _ *Facts) { r.Verdict = "approve" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := baseFacts()
			r := goodReview(f, 20)
			mut(&r, &f)
			f.Reviews = []Review{r}
			mustOutcome(t, Evaluate(f), Wait, WaitReview)
		})
	}
}

func TestReviewByAnotherModelParks(t *testing.T) {
	f := baseFacts()
	r := goodReview(f, 20)
	r.Observed = bossP
	f.Reviews = []Review{r}
	mustOutcome(t, Evaluate(f), Park, ReasonVerifierChanged)
}

// A blocker poisons its round: a later clean review does not clear it (only a new round does).
func TestBlockerPoisonsTheRound(t *testing.T) {
	f := baseFacts()
	b := goodReview(f, 20)
	b.Verdict, b.ReasonCode = VerdictBlock, "too-easy"
	f.Reviews = []Review{b, goodReview(f, 21)}
	mustOutcome(t, Evaluate(f), Park, ReasonVerifierBlocked)
	// a new round: the old block is bound elsewhere and does not apply, the old ok does not satisfy
	f.Round = Round{N: 2, FreezeSeq: 30, Digest: "d2", CloseSeq: 5}
	for i := range f.Evidence {
		f.Evidence[i].Round, f.Evidence[i].FreezeSeq, f.Evidence[i].Digest = 2, 30, "d2"
		f.Evidence[i].Seq += 20
	}
	mustOutcome(t, Evaluate(f), Wait, WaitReview)
	f.Reviews = append(f.Reviews, goodReview(f, 60))
	mustOutcome(t, Evaluate(f), Commit, "")
}

func TestEvidenceConditions(t *testing.T) {
	t.Run("a failed criterion parks even with a clean review", func(t *testing.T) {
		f := baseFacts()
		f.Evidence[1].Result = "fail"
		f.Reviews = []Review{goodReview(f, 20)}
		mustOutcome(t, Evaluate(f), Park, ReasonCriteriaFailed)
	})
	for res, want := range map[string]Reason{"error": ReasonCriterionError, "timeout": ReasonCriterionError, "oversize": ReasonCriterionError,
		"tainted": ReasonTainted, "tampered": ReasonTampered, "weird": ReasonCriterionError} {
		t.Run("result "+res, func(t *testing.T) {
			f := baseFacts()
			f.Evidence[0].Result = res
			f.Reviews = []Review{goodReview(f, 20)}
			mustOutcome(t, Evaluate(f), Park, want)
		})
	}
	stale := map[string]func(e *Evidence){
		"earlier round":      func(e *Evidence) { e.Round = 0 },
		"other freeze point": func(e *Evidence) { e.FreezeSeq = 9 },
		"other digest":       func(e *Evidence) { e.Digest = "dx" },
		"other criterion":    func(e *Evidence) { e.CriterionHash = "hx" },
		"other generation":   func(e *Evidence) { e.Generation = 7 },
		"advisory":           func(e *Evidence) { e.Advisory = true },
	}
	for name, mut := range stale {
		t.Run("evidence from "+name+" is not evidence", func(t *testing.T) {
			f := baseFacts()
			mut(&f.Evidence[0])
			f.Reviews = []Review{goodReview(f, 20)}
			mustOutcome(t, Evaluate(f), Wait, WaitEvidence)
		})
	}
	t.Run("evidence under another verifier pin", func(t *testing.T) {
		f := baseFacts()
		f.Evidence[0].Verifier = bossP
		f.Reviews = []Review{goodReview(f, 20)}
		mustOutcome(t, Evaluate(f), Park, ReasonVerifierChanged)
	})
	t.Run("work admitted after the close", func(t *testing.T) {
		f := baseFacts()
		f.Reviews = []Review{goodReview(f, 20)}
		f.WorkRowsAfterClose = 1
		mustOutcome(t, Evaluate(f), Park, ReasonInternalError)
	})
	t.Run("recheck not done", func(t *testing.T) {
		f := baseFacts()
		f.Reviews = []Review{goodReview(f, 20)}
		f.RecheckOK = false
		mustOutcome(t, Evaluate(f), Wait, WaitRecheck)
	})
	t.Run("not verifying", func(t *testing.T) {
		f := baseFacts()
		f.Reviews = []Review{goodReview(f, 20)}
		f.State = "finish-parked"
		mustOutcome(t, Evaluate(f), Park, ReasonInternalError)
	})
}

func TestRubricNeedsABoundApproval(t *testing.T) {
	f := baseFacts()
	f.Charter.Criteria = append(f.Charter.Criteria, Criterion{ID: "r1", Hash: "hr", Rubric: true})
	f.Reviews = []Review{goodReview(f, 20)}
	mustOutcome(t, Evaluate(f), Wait, WaitRubric)
	d := RubricDecision{Generation: 1, CharterHash: "ch-1", Round: 1, FreezeSeq: 10, Digest: "dOLD", CriterionID: "r1", CriterionHash: "hr", Approve: true}
	f.Decisions = []RubricDecision{d}
	mustOutcome(t, Evaluate(f), Wait, WaitRubric) // an approval of another digest is void
	d.Digest = "d1"
	f.Decisions = []RubricDecision{d}
	mustOutcome(t, Evaluate(f), Commit, "")
	d.Approve = false
	f.Decisions = []RubricDecision{d}
	mustOutcome(t, Evaluate(f), Park, ReasonRubricRejected)
}

// Q3/Q6: a charter whose verifier is absent, moving or not distinct cannot finish.
func TestCharterWithoutDistinctPinnedVerifierCannotFinish(t *testing.T) {
	mod := map[string]func(c *Charter){
		"no criteria":    func(c *Charter) { c.Criteria = nil },
		"empty pin":      func(c *Charter) { c.Verifier = Pin{} },
		"missing policy": func(c *Charter) { c.Verifier.PolicyHash = "" },
		"moving alias":   func(c *Charter) { c.Verifier.Model = "latest" },
		"moving version": func(c *Charter) { c.Verifier.Version = "Latest" },
		"same as boss": func(c *Charter) {
			c.Verifier = Pin{Provider: "PROV-A", Model: "Boss-1", Version: "v9", PolicyHash: "p"}
		},
		"in the roster": func(c *Charter) {
			c.Roster = append(c.Roster, Pin{Provider: "prov-b", Model: "verifier-1", Version: "other"})
		},
	}
	for name, m := range mod {
		t.Run(name, func(t *testing.T) {
			f := baseFacts()
			m(&f.Charter)
			if f.Charter.CannotFinish() == "" {
				t.Fatal("the charter can finish")
			}
			f.Reviews = []Review{goodReview(f, 20)}
			if Evaluate(f).Outcome == Commit {
				t.Fatal("committed")
			}
		})
	}
	if baseFacts().Charter.CannotFinish() != "" {
		t.Fatal("the base charter cannot finish")
	}
	// the same model name at another provider is a different model
	c := baseFacts().Charter
	c.Verifier = Pin{Provider: "prov-z", Model: "boss-1", Version: "1", PolicyHash: "p"}
	if c.CannotFinish() != "" {
		t.Fatalf("a different provider was treated as the same model: %s", c.CannotFinish())
	}
}

func TestResultsDigestCoversEveryField(t *testing.T) {
	f := baseFacts()
	base := ResultsDigest(f.Evidence)
	for name, mut := range map[string]func(e *Evidence){
		"result": func(e *Evidence) { e.Result = "fail" }, "stdout": func(e *Evidence) { e.StdoutDigest = "x" },
		"stderr": func(e *Evidence) { e.StderrDigest = "x" }, "criterion": func(e *Evidence) { e.CriterionHash = "x" },
	} {
		g := baseFacts()
		mut(&g.Evidence[0])
		if ResultsDigest(g.Evidence) == base {
			t.Fatalf("%s is not covered", name)
		}
	}
}
