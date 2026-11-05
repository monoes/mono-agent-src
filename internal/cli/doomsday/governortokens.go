package doomsday

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/queue"
)

// GovernedTokens is the Stop order's token and queue seam with the governor's queue cancelled alongside the host's pending
// sends (note B3, seam S6). It adds a cancel and nothing else: revocation is the inner seam's, and the governor's cancel is
// best effort on its own goroutine with a bound, because a wedged or missing governor table must never keep Stop from completing. That is safe:
// no row of a run that is not running can be admitted (the migration's trigger refuses it), so a queue row that could not be
// cancelled here can never start anything.
type GovernedTokens struct {
	Inner interface {
		RevokeTokens(ctx context.Context, gen uint64) error
		CancelQueue(ctx context.Context, gen uint64) error
	}
	DB      *sql.DB
	Profile string
	Keys    governor.Keyer
	Logf    func(format string, args ...any)
}

func (g GovernedTokens) RevokeTokens(ctx context.Context, gen uint64) error {
	return g.Inner.RevokeTokens(ctx, gen)
}

// CancelQueue cancels the host's pending sends (their error is Stop's answer) and starts the governor's cancel on a goroutine
// that Stop does not wait for: a wedged key store or a held governor table must not delay Stop's later steps (the unit end).
// The goroutine is bounded by govCallTimeout and recovers its panics. Nothing depends on it finishing: a queued row of a run
// that is not running can never be admitted.
func (g GovernedTokens) CancelQueue(ctx context.Context, gen uint64) error {
	err := g.Inner.CancelQueue(ctx, gen)
	go func() {
		defer func() { _ = recover() }()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), govCallTimeout)
		defer cancel()
		if _, e := queue.New(g.DB, g.Keys).CancelAll(cctx, g.Profile, int64(gen)); e != nil && g.Logf != nil {
			g.Logf("doomsday governor: cancelling the queue: %v", e)
		}
	}()
	return err
}
