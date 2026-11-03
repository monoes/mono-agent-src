package finish

import (
	"context"
	"errors"
	"sync"
	"testing"
)

var (
	bg        = context.Background()
	errCrash  = errors.New("process died here")
	verifierP = Pin{Provider: "prov-b", Model: "verifier-1", Version: "2026-10-01", PolicyHash: "pol-1"}
	bossP     = Pin{Provider: "prov-a", Model: "boss-1", Version: "2026-09-01", PolicyHash: "pol-0"}
	workerP   = Pin{Provider: "prov-a", Model: "worker-1", Version: "2026-09-01", PolicyHash: "pol-0"}
)

// baseFacts: a verifying run, two machine criteria with passing evidence of round 1, no review yet.
func baseFacts() Facts {
	ch := Charter{Hash: "ch-1", Verifier: verifierP, Boss: bossP, Roster: []Pin{workerP}, MaxRounds: 3,
		Criteria: []Criterion{{ID: "c1", Hash: "h1"}, {ID: "c2", Hash: "h2"}}}
	r := Round{N: 1, FreezeSeq: 10, Digest: "d1", CloseSeq: 5}
	ev := func(seq int64, c Criterion) Evidence {
		return Evidence{Seq: seq, Generation: 1, CharterHash: ch.Hash, Round: r.N, FreezeSeq: r.FreezeSeq, Digest: r.Digest,
			CriterionID: c.ID, CriterionHash: c.Hash, Result: "pass", Verifier: verifierP, StdoutDigest: "o" + c.ID, StderrDigest: "e" + c.ID}
	}
	return Facts{Generation: 1, State: "verifying", Charter: ch, Round: r, RecheckOK: true,
		Evidence: []Evidence{ev(11, ch.Criteria[0]), ev(12, ch.Criteria[1])}}
}

// goodReview is a complete, bound, clean review written after the evidence.
func goodReview(f Facts, seq int64) Review {
	return Review{Seq: seq, Generation: f.Generation, CharterHash: f.Charter.Hash, Round: f.Round.N, FreezeSeq: f.Round.FreezeSeq,
		Digest: f.Round.Digest, State: ReviewComplete, Verdict: VerdictOK, Observed: f.Charter.Verifier,
		ResultsDigest: ResultsDigest(f.roundEvidence())}
}

// fakeLedger is an in-memory ledger whose Commit re-evaluates the predicate under its lock, like the store's
// conditional insert; a "crash" is the Finisher's hook returning errCrash (the rows written so far stay).
type fakeLedger struct {
	mu        sync.Mutex
	f         Facts
	seq       int64
	committed bool
	parked    *Verdict
}

func newLedger(f Facts) *fakeLedger { return &fakeLedger{f: f, seq: 100} }

func (l *fakeLedger) Facts(context.Context) (Facts, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f, nil
}

func (l *fakeLedger) RecordReview(_ context.Context, r Review) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	r.Seq = l.seq
	l.f.Reviews = append(append([]Review{}, l.f.Reviews...), r)
	return nil
}

func (l *fakeLedger) Commit(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if Evaluate(l.f).Outcome != Commit {
		return ErrCommitRefused
	}
	l.committed = true
	return nil
}

func (l *fakeLedger) Park(_ context.Context, v Verdict) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.parked = &v
	return nil
}

type fakeVerifier struct {
	calls  int
	res    ReviewResult
	err    error
	inputs []ReviewInput
}

func (v *fakeVerifier) Review(_ context.Context, in ReviewInput) (ReviewResult, error) {
	v.calls++
	v.inputs = append(v.inputs, in)
	return v.res, v.err
}

func okVerifier() *fakeVerifier {
	return &fakeVerifier{res: ReviewResult{Observed: verifierP, Verdict: VerdictOK, ReasonCode: "looks-fine"}}
}

func mustOutcome(t *testing.T, v Verdict, o Outcome, r Reason) {
	t.Helper()
	if v.Outcome != o || (r != "" && v.Reason != r) {
		t.Fatalf("got %+v, want outcome %d reason %q", v, o, r)
	}
}
