package sstore

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// LatestEpoch is the epoch with the highest sequence of the run; ok is false when there is none. After a restart the
// epoch engine continues from it (an open one is resumed, never re-opened under a new id).
func (s *Store) LatestEpoch(ctx context.Context, profile string, gen int64) (e types.Epoch, ok bool, err error) {
	all, err := s.ListEpochs(ctx, profile, gen)
	if err != nil || len(all) == 0 {
		return e, false, err
	}
	return all[len(all)-1], true, nil
}

// NextEpochSeq is the sequence the next epoch takes: latest+1, or 1.
func (s *Store) NextEpochSeq(ctx context.Context, profile string, gen int64) (int64, error) {
	e, ok, err := s.LatestEpoch(ctx, profile, gen)
	if err != nil || !ok {
		return 1, err
	}
	return e.Seq + 1, nil
}

// OpenDirections are the directions still being worked: proposed, active or stalled (not finished, not pruned).
func (s *Store) OpenDirections(ctx context.Context, profile string, gen int64) ([]types.Direction, error) {
	return s.ListDirections(ctx, profile, gen, types.DirProposed, types.DirActive, types.DirStalled)
}

// ParkedFinishRounds counts the epochs of the run that were started because a finish was parked. The policy stops
// requesting a finish at types.MaxParkedFinishRounds; counting stored epochs keeps the bound across restarts.
func (s *Store) ParkedFinishRounds(ctx context.Context, profile string, gen int64) (int, error) {
	all, err := s.ListEpochs(ctx, profile, gen)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range all {
		if e.Trigger == types.TriggerFinishParked {
			n++
		}
	}
	return n, nil
}
