//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/queue"
)

// GovernedTokens is the Stop order's token and queue seam with the governor's queue cancelled alongside the host's pending
// sends (note B3, seam S6). It adds a cancel and nothing else: revocation is the inner seam's, and the governor's cancel is
// best effort with a bound, because a wedged or missing governor table must never keep Stop from completing. That is safe:
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

// CancelQueue cancels the host's pending sends first (their error is Stop's), then the governor's queued launches.
func (g GovernedTokens) CancelQueue(ctx context.Context, gen uint64) error {
	err := g.Inner.CancelQueue(ctx, gen)
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), govCallTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() { // a driver that does not honour the context cannot hold Stop either
		defer close(done)
		defer func() { _ = recover() }()
		if _, e := queue.New(g.DB, g.Keys).CancelAll(cctx, g.Profile, int64(gen)); e != nil && g.Logf != nil {
			g.Logf("doomsday governor: cancelling the queue: %v", e)
		}
	}()
	select {
	case <-done:
	case <-time.After(govCallTimeout + time.Second):
		if g.Logf != nil {
			g.Logf("doomsday governor: cancelling the queue timed out; Stop goes on")
		}
	}
	return err
}
