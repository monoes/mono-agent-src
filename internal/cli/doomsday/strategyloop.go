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
	// AfterStep, when set, runs after every tick on the same goroutine (the resource governor's pump and report ride on
	// this loop). It must honour ctx and recover its own panics; it is not called once ctx is done.
	AfterStep func(ctx context.Context)

	// Active is the cheap precheck of every tick: whether the profile has a run that is not quiescent. Nil means "always" (a
	// test's loop). It must not build, pin, read a key or log; false makes the tick do nothing else.
	Active func(ctx context.Context) bool

	host *StrategyHost
}

func (l *StrategyLoop) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// ErrStrategyPanicked is what a recovered panic in a tick or in building the host reports. The panic value is not carried:
// it may hold text the layer must not repeat.
var ErrStrategyPanicked = errors.New("strategy: a tick panicked and was recovered; the host is rebuilt for the next one")

// Step is one tick: build the host if there is none, then Tick it. It returns what the tick did. A panic anywhere inside
// (the store, the judge adapter, a seam) is recovered: the tick is over, the host is dropped so the next tick builds a
// fresh one from the database, the coder host process is not touched, and the log line is class-level only.
func (l *StrategyLoop) Step(ctx context.Context) (tk HostTick, err error) {
	if err := ctx.Err(); err != nil {
		return HostTick{}, err // no tick after the loop was told to stop
	}
	defer func() {
		if r := recover(); r != nil {
			l.host = nil
			l.logf("doomsday strategy: a tick panicked (%T) and was recovered", r)
			tk, err = HostTick{Idle: "parked"}, ErrStrategyPanicked
		}
	}()
	if l.Active != nil && !l.Active(ctx) {
		return HostTick{Idle: "no-run"}, nil // the whole tick with no run: one read-only count, nothing built, pinned, keyed or logged
	}
	if l.host == nil {
		h, err := l.Build(ctx)
		if err != nil {
			return HostTick{}, err
		}
		l.host = h
	}
	return l.host.Tick(ctx)
}

// Start runs the loop in its own goroutine under a context derived from parent. The returned stop cancels it and WAITS until
// the goroutine has returned: after stop there is no tick in flight and none will start, so the caller may close the
// database. Calling stop twice is fine.
func (l *StrategyLoop) Start(parent context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
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
		if l.AfterStep != nil && ctx.Err() == nil {
			l.AfterStep(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}
