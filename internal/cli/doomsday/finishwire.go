package doomsday

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The finish ledger as a witnessed writer (note 13.5 section 3, note 13.6 S1). The store's FinishLedger writes its chained
// rows in transactions of its own; routed around the sequencer they would leave the database ahead of the witness, and the
// next freshness check would fail the generation closed at row 5. Every write of the ledger therefore runs inside
// Sequencer.Sequence: freshness first, a pending line, the store's conditional insert, the committed line.
//
// None of these transitions is a Stop-intent kind, so the pending line is not marked ending: a crash before the insert is
// an abandoned range (row 2) and the step is simply repeated, which is what the finish's idempotence relies on.

// SeqFinish is the finish.Ledger of one generation with every write sequenced.
type SeqFinish struct {
	S       *SeqStore
	L       *store.FinishLedger
	Profile string
	Gen     int64
}

var _ finish.Ledger = (*SeqFinish)(nil)

// FinishLedger returns the sequenced ledger of one generation.
func (s *SeqStore) FinishLedger(profile string, gen int64) *SeqFinish {
	return &SeqFinish{S: s, L: s.SQLStore.FinishLedger(profile, gen), Profile: profile, Gen: gen}
}

// FinishFacts reads the facts of a generation's finish from a verified chain.
func (s *SeqStore) FinishFacts(ctx context.Context, profile string, gen int64) (finish.Facts, error) {
	return s.SQLStore.FinishLedger(profile, gen).Facts(ctx)
}

func (f *SeqFinish) seq(ctx context.Context, kind string, ended bool, fn func(context.Context) error) error {
	return f.S.sequence(ctx, f.Gen, kind, false, ended, fn)
}

func (f *SeqFinish) Facts(ctx context.Context) (finish.Facts, error) { return f.L.Facts(ctx) }

func (f *SeqFinish) Freeze(ctx context.Context, digest, snapshot string) (round int64, err error) {
	err = f.seq(ctx, "finish.freeze", false, func(ctx context.Context) (e error) {
		round, e = f.L.Freeze(ctx, digest, snapshot)
		return e
	})
	return round, err
}

func (f *SeqFinish) RecordEvidence(ctx context.Context, e finish.Evidence) error {
	return f.seq(ctx, "finish.evidence", false, func(ctx context.Context) error { return f.L.RecordEvidence(ctx, e) })
}

func (f *SeqFinish) RecordDecision(ctx context.Context, d finish.RubricDecision) error {
	return f.seq(ctx, "finish.decision", false, func(ctx context.Context) error { return f.L.RecordDecision(ctx, d) })
}

func (f *SeqFinish) RecordRecheck(ctx context.Context, ok bool) error {
	return f.seq(ctx, "finish.recheck", false, func(ctx context.Context) error { return f.L.RecordRecheck(ctx, ok) })
}

func (f *SeqFinish) RecordReview(ctx context.Context, r finish.Review) error {
	return f.seq(ctx, "finish.review", false, func(ctx context.Context) error { return f.L.RecordReview(ctx, r) })
}

// Commit is the finish-commit transition: `finished` is an ended state, so a successful commit is recorded as the
// generation's end like a Stop (the anchor is marked ended again, which is idempotent). The store decides the commit
// inside its own conditional insert; the sequencer only makes it witnessed.
func (f *SeqFinish) Commit(ctx context.Context) error {
	return f.seq(ctx, "finish.commit", true, func(ctx context.Context) error { return f.L.Commit(ctx) })
}

func (f *SeqFinish) Park(ctx context.Context, v finish.Verdict) error {
	return f.seq(ctx, "finish.park", false, func(ctx context.Context) error { return f.L.Park(ctx, v) })
}

func (f *SeqFinish) Abort(ctx context.Context, by string) error {
	return f.seq(ctx, "finish.stop", false, func(ctx context.Context) error { return f.L.Abort(ctx, by) })
}

// finishView is the run's finish as `doomsday status` shows it.
type finishView interface {
	FinishFacts(ctx context.Context, profile string, gen int64) (finish.Facts, error)
}

var _ finishView = (*SeqStore)(nil)

// FinishStatus names the state of a generation's finish ("" when the run is not a finish): quiescent while the freeze is
// awaited, then verifying, finish-parked (with the closed reason) or finished.
func FinishStatus(ctx context.Context, v finishView, profile string, run store.Run) (state, reason string, round int64, err error) {
	if run.State != store.StateQuiescent || run.Cause != store.FinishCause {
		return "", "", 0, nil
	}
	f, err := v.FinishFacts(ctx, profile, run.Generation)
	if err != nil {
		return "", "", 0, err
	}
	return f.State, string(f.ParkReason), f.Round.N, nil
}
