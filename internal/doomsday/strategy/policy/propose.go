package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/novelty"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Gate is the novelty gate; *novelty.Checker satisfies it.
type Gate interface {
	Check(ctx context.Context, props []types.Proposal, unchecked int) ([]novelty.Result, int)
}

// Ledger is the slice of the strategy store the intake writes through; *sstore.Store satisfies it.
type Ledger interface {
	AcceptProposal(ctx context.Context, d types.Direction, n types.NoveltyRow) error
	RecordRefusal(ctx context.Context, n types.NoveltyRow) error
}

// RunState tells whether the run is still running, read from the run table (not a local flag). Any error or false
// refuses every proposal.
type RunState interface {
	Running(ctx context.Context, profile string, generation int64) (bool, error)
}

// ErrRunNotRunning: no proposal is taken once the run is not running (or it cannot be told).
var ErrRunNotRunning = errors.New("policy: the run is not running; no proposal accepted")

// Intake is what happened to one submitted proposal.
type Intake struct {
	Index       int
	Outcome     types.NoveltyOutcome // empty when the proposal was invalid or not looked at
	DirectionID string               // the new direction, when accepted
	MatchedID   string               // the row a refusal cites
	Reason      string               // class-level text for the caller
	Err         error                // invalid, over the batch cap, or the store failed
}

// Accepted reports whether a direction was created.
func (i Intake) Accepted() bool { return i.DirectionID != "" && i.Err == nil }

// Proposer takes planner proposals in: validate (caps), novelty gate, persist. It decides nothing about ranking or finish.
type Proposer struct {
	Profile    string
	Generation int64
	Gate       Gate
	Ledger     Ledger
	Run        RunState
	NewID      func() (string, error) // direction ids; injected so tests are deterministic
}

// Submit processes proposals of one epoch. unchecked is how many accepted-unchecked proposals the epoch already has;
// the new count is returned. A per-proposal problem is reported in its Intake and does not stop the others; only a
// run that is not running stops all of them.
func (p *Proposer) Submit(ctx context.Context, epochSeq int64, unchecked int, props []types.Proposal) ([]Intake, int, error) {
	if p.Gate == nil || p.Ledger == nil || p.Run == nil || p.NewID == nil {
		return nil, unchecked, errors.New("policy: proposer is not fully wired")
	}
	if ok, err := p.Run.Running(ctx, p.Profile, p.Generation); err != nil || !ok {
		if err != nil {
			return nil, unchecked, fmt.Errorf("%w: %v", ErrRunNotRunning, err)
		}
		return nil, unchecked, ErrRunNotRunning
	}
	out := make([]Intake, len(props))
	var valid []types.Proposal
	var at []int
	for i, pr := range props {
		out[i].Index = i
		if err := pr.Validate(); err != nil {
			out[i].Err, out[i].Reason = err, "proposal failed validation"
			continue
		}
		valid, at = append(valid, pr), append(at, i)
	}
	results, unchecked := p.Gate.Check(ctx, valid, unchecked)
	for k, r := range results {
		if k >= len(at) {
			break
		}
		i := at[k]
		out[i].Outcome, out[i].MatchedID, out[i].Reason, out[i].Err = r.Outcome, r.MatchedID, r.Reason, r.Err
		if r.Err != nil || r.Outcome == "" {
			out[i].Outcome = ""
			if out[i].Err == nil {
				out[i].Err = errors.New("policy: gate gave no verdict")
			}
			continue
		}
		p.persist(ctx, epochSeq, valid[k], r, &out[i])
	}
	return out, unchecked, nil
}

func (p *Proposer) persist(ctx context.Context, epochSeq int64, pr types.Proposal, r novelty.Result, in *Intake) {
	row := types.NoveltyRow{Profile: p.Profile, Generation: p.Generation, EpochSeq: epochSeq, KeyHash: r.KeyHash, MatchedID: r.MatchedID, Outcome: r.Outcome}
	switch r.Outcome {
	case types.NoveltyAccepted, types.NoveltyAcceptedUnchecked:
		id, err := p.NewID()
		if err != nil {
			in.Outcome, in.Err = "", fmt.Errorf("policy: direction id: %w", err)
			return
		}
		d := types.Direction{Profile: p.Profile, Generation: p.Generation, ID: id, KeyHash: r.KeyHash, Title: pr.Title,
			State: types.DirProposed, ProposerSession: pr.ProposerSession, CreatedEpoch: epochSeq}
		err = p.Ledger.AcceptProposal(ctx, d, row)
		var dup *sstore.DuplicateKeyError
		switch {
		case err == nil:
			in.DirectionID = id
		case errors.As(err, &dup):
			p.refuse(ctx, in, row, types.NoveltyRefusedDirection, dup.ExistingID, "key already stored in this run")
		case errors.Is(err, sstore.ErrUncheckedLimit):
			p.refuse(ctx, in, row, types.NoveltyRefusedCorpus, "", "unchecked allowance of this epoch is used")
		default:
			in.Outcome, in.Err = "", err
		}
	default:
		if err := p.Ledger.RecordRefusal(ctx, row); err != nil {
			in.Err = err
		}
	}
}

// refuse records a refusal that the store, not the gate, produced (a race with another submitter or the unchecked cap).
func (p *Proposer) refuse(ctx context.Context, in *Intake, row types.NoveltyRow, o types.NoveltyOutcome, matched, why string) {
	row.Outcome, row.MatchedID, row.DirectionID = o, matched, ""
	in.Outcome, in.MatchedID, in.Reason = o, matched, why
	if err := p.Ledger.RecordRefusal(ctx, row); err != nil {
		in.Err = err
	}
}
