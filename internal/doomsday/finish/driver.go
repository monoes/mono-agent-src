package finish

import (
	"context"
	"errors"
	"fmt"
)

// ErrCommitRefused: the ledger re-evaluated the predicate inside its conditional insert and refused.
var ErrCommitRefused = errors.New("finish: the commit predicate does not hold; nothing was inserted")

// Ledger is the chained store as the finish needs it (the store package satisfies it inside the sequencer's
// critical section). Commit must evaluate Evaluate over the facts it reads under its own locks, and insert
// only on Commit, so a caller cannot finish by calling it.
type Ledger interface {
	Facts(ctx context.Context) (Facts, error)
	// RecordReview appends a chained review row (started or complete).
	RecordReview(ctx context.Context, r Review) error
	// Commit is the finish-commit transition.
	Commit(ctx context.Context) error
	// Park is the finish-park transition with a named reason.
	Park(ctx context.Context, v Verdict) error
}

// ReviewInput is what the verifier is shown: the round's machine results (excerpts for the person, digests for
// the harness) and the round identity.
type ReviewInput struct {
	Round   Round
	Results []Evidence
}

// ReviewResult is the verifier's answer reduced to the closed enumeration, plus the tuple the provider reports.
// Free text never reaches the harness.
type ReviewResult struct {
	Observed   Pin
	Verdict    string
	ReasonCode string
}

// Verifier calls the pinned model. It must not fall back to another model; an error means unavailable.
type Verifier interface {
	Review(ctx context.Context, in ReviewInput) (ReviewResult, error)
}

// Finisher drives one finish step at a time. Every step is safe to repeat after a crash at any point: all
// state is in the ledger.
type Finisher struct {
	Ledger   Ledger
	Verifier Verifier
	// Hook names the crash points ("review.started", "review.called", "review.complete", "commit"); a test
	// makes a point return an error to model the process dying there.
	Hook func(point string) error
}

func (fn Finisher) at(p string) error {
	if fn.Hook != nil {
		return fn.Hook(p)
	}
	return nil
}

func (f Facts) machineEvidenceComplete() bool {
	for _, c := range f.Charter.Criteria {
		if !c.Rubric && len(f.evidenceFor(c)) != 1 {
			return false
		}
	}
	return true
}

// Step reads the facts, runs the review if the machine results are in and no bound review covers them, and
// then commits, parks or waits according to Evaluate.
func (fn Finisher) Step(ctx context.Context) (Verdict, error) {
	for range 3 {
		f, err := fn.Ledger.Facts(ctx)
		if err != nil {
			return Verdict{}, err
		}
		switch f.State {
		case StateFinished: // a repeat after a crash at the commit: the commit is a fact, never a second row
			return Verdict{Outcome: Commit}, nil
		case StateParked:
			return Verdict{Outcome: Park, Reason: f.ParkReason, Detail: "the finish is parked for this round"}, nil
		}
		if rv, need := f.reviewVerdict(); need && rv.Outcome == Wait && f.State == StateVerifying &&
			f.Charter.CannotFinish() == "" && f.machineEvidenceComplete() && f.WorkRowsAfterClose == 0 {
			if pv, err := fn.runReview(ctx, f); err != nil || pv.Outcome == Park {
				return pv, err
			}
			continue // re-read: the review row is part of the facts now
		}
		v := Evaluate(f)
		switch v.Outcome {
		case Commit:
			if err := fn.at("commit"); err != nil {
				return v, err
			}
			return v, fn.Ledger.Commit(ctx)
		case Park:
			return v, fn.Ledger.Park(ctx, v)
		}
		return v, nil
	}
	return Verdict{}, fmt.Errorf("finish: the review rows did not settle")
}

// runReview records started, asks the pinned verifier once, checks what it reports against the pin and records
// complete. A park verdict means the finish was parked here; an error means the step stopped (a crash point).
func (fn Finisher) runReview(ctx context.Context, f Facts) (Verdict, error) {
	row := Review{Generation: f.Generation, CharterHash: f.Charter.Hash, Round: f.Round.N, FreezeSeq: f.Round.FreezeSeq, Digest: f.Round.Digest}
	started := row
	started.State = ReviewStarted
	if err := fn.Ledger.RecordReview(ctx, started); err != nil {
		return Verdict{}, err
	}
	if err := fn.at("review.started"); err != nil {
		return Verdict{}, err
	}
	ev := f.roundEvidence()
	res, err := fn.Verifier.Review(ctx, ReviewInput{Round: f.Round, Results: ev})
	if err := fn.at("review.called"); err != nil {
		return Verdict{}, err
	}
	park := func(r Reason, d string) (Verdict, error) {
		v := Verdict{Park, r, d}
		return v, fn.Ledger.Park(ctx, v)
	}
	switch {
	case err != nil:
		return park(ReasonVerifierUnavailable, "the verifier call failed")
	case res.Observed != f.Charter.Verifier:
		return park(ReasonVerifierChanged, "the provider reported a different model, version or policy than the pin")
	case res.Verdict != VerdictOK && res.Verdict != VerdictBlock:
		return park(ReasonVerifierUnavailable, "the verifier answered outside the closed enumeration")
	}
	done := row
	done.State, done.Verdict, done.ReasonCode, done.Observed = ReviewComplete, res.Verdict, cleanReason(res.ReasonCode), res.Observed
	done.ResultsDigest = ResultsDigest(ev)
	if err := fn.Ledger.RecordReview(ctx, done); err != nil {
		return Verdict{}, err
	}
	return Verdict{}, fn.at("review.complete")
}

// cleanReason keeps a reason code to a short [a-z0-9-] token; anything else is dropped, so model text cannot
// travel through the decision path (section 9).
func cleanReason(s string) string {
	if len(s) == 0 || len(s) > 32 {
		return "unspecified"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "unspecified"
		}
	}
	return s
}
