package novelty

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Work bounds. A hostile proposal list costs at most MaxBatch normalisations and MaxBatch*3 corpus calls, and a corpus
// answer is read for at most MaxHits entries, whatever it returns.
const (
	MaxBatch         = 32
	MaxHits          = 16
	DefaultThreshold = 0.85
)

// Match names a stored row a proposal collides with.
type Match struct{ ID string }

// Hit is one similarity answer from memory or knowledge. Score is in [0,1], 1 meaning identical.
type Hit struct {
	ID     string
	Score  float64
	Source string // "memory" or "knowledge"; informational
}

// Corpus is everything the gate looks things up in. The caller supplies it; this package opens no file and no socket.
// Each method may fail; a failure means "could not check", never "novel".
type Corpus interface {
	// LedgerKey finds an earlier proposal of this run with the same key.
	LedgerKey(ctx context.Context, key [32]byte) (Match, bool, error)
	// DirectionKey finds an open or pruned direction of this run with the same key.
	DirectionKey(ctx context.Context, key [32]byte) (Match, bool, error)
	// Similar returns memory and knowledge entries close to the normalised text, best first.
	Similar(ctx context.Context, normalised string) ([]Hit, error)
}

// Checker holds the threshold. It has no state of its own: the per-epoch unchecked count is an input and an output.
type Checker struct {
	corpus    Corpus
	threshold float64
}

// New builds a Checker. threshold must be in (0,1]; 0 means DefaultThreshold.
func New(c Corpus, threshold float64) (*Checker, error) {
	if c == nil {
		return nil, errors.New("novelty: nil corpus")
	}
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	if math.IsNaN(threshold) || threshold <= 0 || threshold > 1 {
		return nil, fmt.Errorf("novelty: threshold %v outside (0,1]", threshold)
	}
	return &Checker{corpus: c, threshold: threshold}, nil
}

// Result is the verdict on one proposal. Err is set (and Outcome empty) only for a proposal that cannot be keyed or that
// was not looked at because the batch cap was exceeded.
type Result struct {
	Index      int
	Normalised string
	KeyHash    [32]byte
	Outcome    types.NoveltyOutcome
	MatchedID  string // the ledger row, direction or corpus entry a refusal cites; "" otherwise
	Reason     string // short class-level text for the ledger
	Err        error
}

// ErrBatchCap marks proposals beyond MaxBatch.
var ErrBatchCap = errors.New("novelty: proposal list over the batch cap")

// Check judges proposals in order. unchecked is how many accepted-unchecked proposals this epoch already has; the new
// count is returned and never exceeds types.MaxUncheckedPerEpoch. A proposal whose key repeats an earlier accepted one in
// the same list is refused-ledger citing "batch:<index>", so a list cannot sneak the same idea in twice.
func (c *Checker) Check(ctx context.Context, props []types.Proposal, unchecked int) ([]Result, int) {
	if unchecked < 0 {
		unchecked = 0
	}
	res := make([]Result, len(props))
	inBatch := make(map[[32]byte]int, min(len(props), MaxBatch))
	for i, p := range props {
		r := Result{Index: i}
		if i >= MaxBatch {
			r.Err = ErrBatchCap
			res[i] = r
			continue
		}
		var err error
		r.Normalised, r.KeyHash, err = Key(p.Title)
		if err != nil {
			r.Err = err
			res[i] = r
			continue
		}
		if first, dup := inBatch[r.KeyHash]; dup {
			r.Outcome, r.MatchedID, r.Reason = types.NoveltyRefusedLedger, fmt.Sprintf("batch:%d", first), "repeats an earlier proposal in the same list"
			res[i] = r
			continue
		}
		unchecked = c.judge(ctx, &r, unchecked)
		if r.Outcome == types.NoveltyAccepted || r.Outcome == types.NoveltyAcceptedUnchecked {
			inBatch[r.KeyHash] = i
		}
		res[i] = r
	}
	return res, unchecked
}

func (c *Checker) judge(ctx context.Context, r *Result, unchecked int) int {
	var failed bool
	if m, ok, err := c.corpus.LedgerKey(ctx, r.KeyHash); err != nil {
		failed = true
	} else if ok {
		r.Outcome, r.MatchedID, r.Reason = types.NoveltyRefusedLedger, m.ID, "same key as an earlier proposal"
		return unchecked
	}
	if m, ok, err := c.corpus.DirectionKey(ctx, r.KeyHash); err != nil {
		failed = true
	} else if ok {
		r.Outcome, r.MatchedID, r.Reason = types.NoveltyRefusedDirection, m.ID, "matches an open or pruned direction"
		return unchecked
	}
	hits, err := c.corpus.Similar(ctx, r.Normalised)
	if err != nil {
		failed = true
	}
	if len(hits) > MaxHits {
		hits = hits[:MaxHits]
	}
	for _, h := range hits {
		// Written as a negation so a NaN score refuses instead of passing.
		if !(h.Score < c.threshold) {
			r.Outcome, r.MatchedID, r.Reason = types.NoveltyRefusedCorpus, h.ID, "memory or knowledge entry at or above the threshold"
			return unchecked
		}
	}
	if !failed {
		r.Outcome = types.NoveltyAccepted
		return unchecked
	}
	if unchecked >= types.MaxUncheckedPerEpoch {
		r.Outcome, r.Reason = types.NoveltyRefusedCorpus, "corpus unavailable and the unchecked allowance of this epoch is used"
		return unchecked
	}
	r.Outcome, r.Reason = types.NoveltyAcceptedUnchecked, "corpus unavailable"
	return unchecked + 1
}
