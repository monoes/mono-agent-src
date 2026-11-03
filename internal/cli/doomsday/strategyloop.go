//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"time"
)

// DefaultStrategyTick is how often the engine's loop ticks the strategy host. The epoch engine has its own floors (an epoch
// is never closer than the code floor to the last), so a faster tick buys nothing; a slower one delays an abandonment after
// a Stop.
const DefaultStrategyTick = 5 * time.Second

// StrategyLoop ticks the strategy host. The host has no goroutine of its own; the engine process runs this loop, and the
// loop is the only thing that ticks it. It builds the host lazily and again after a failure, so a witness that is not ready
// yet only delays the layer, and it never ends the process: an error is logged and the next tick tries again.
type StrategyLoop struct {
	Build func(ctx context.Context) (*StrategyHost, error)
	Every time.Duration
	Logf  func(format string, args ...any)

	host *StrategyHost
}

func (l *StrategyLoop) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// Step is one tick: build the host if there is none, then Tick it. It returns what the tick did.
func (l *StrategyLoop) Step(ctx context.Context) (HostTick, error) {
	if l.host == nil {
		h, err := l.Build(ctx)
		if err != nil {
			return HostTick{}, err
		}
		l.host = h
	}
	return l.host.Tick(ctx)
}

// Run ticks until ctx is done.
func (l *StrategyLoop) Run(ctx context.Context) {
	every := l.Every
	if every <= 0 {
		every = DefaultStrategyTick
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	var lastErr string
	for {
		if _, err := l.Step(ctx); err != nil && !errors.Is(err, context.Canceled) {
			if msg := err.Error(); msg != lastErr { // a persistent fault is logged once, not every tick
				l.logf("doomsday strategy: %v", err)
				lastErr = msg
			}
		} else if err == nil {
			lastErr = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}
