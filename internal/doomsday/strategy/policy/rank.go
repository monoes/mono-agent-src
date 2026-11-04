package policy

import (
	"context"
	"errors"
	"math"
	"sort"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Scorer scores one open direction; higher is better. The host injects one (for example an adapter over the dynorg
// mission evaluation); this package imports none.
type Scorer interface {
	Score(ctx context.Context, d types.Direction) (float64, error)
}

// ErrTooManyDirections bounds the scorer calls of one ranking.
var ErrTooManyDirections = errors.New("policy: too many directions to rank")

// Ranked is one open direction with its score. Err is set when the scorer failed; such a direction ranks last.
type Ranked struct {
	Direction types.Direction
	Score     float64
	Err       error
}

// AcceptsNewWork reports whether a direction may get new spawns or commands. Pruning only closes this door: no
// function in this package, and no interface it declares, can end a running session.
func AcceptsNewWork(s types.DirectionState) bool {
	return s == types.DirProposed || s == types.DirActive || s == types.DirStalled
}

// Rank orders the open directions (finished and pruned are left out) by score, best first. Ties break by the epoch the
// direction was created in, then by id, so the order is deterministic. A failing or NaN score ranks last and is kept.
func Rank(ctx context.Context, dirs []types.Direction, sc Scorer) ([]Ranked, error) {
	if len(dirs) > types.MaxDirections {
		return nil, ErrTooManyDirections
	}
	out := make([]Ranked, 0, len(dirs))
	for _, d := range dirs {
		if !AcceptsNewWork(d.State) {
			continue
		}
		r := Ranked{Direction: d}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.Score, r.Err = sc.Score(ctx, d)
		if r.Err != nil || math.IsNaN(r.Score) {
			r.Score = math.Inf(-1)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Direction.CreatedEpoch != b.Direction.CreatedEpoch {
			return a.Direction.CreatedEpoch < b.Direction.CreatedEpoch
		}
		return a.Direction.ID < b.Direction.ID
	})
	return out, nil
}
