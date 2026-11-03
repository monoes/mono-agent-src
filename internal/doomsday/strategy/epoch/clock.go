// Package epoch is the strategy layer's epoch engine: it decides when a judging round starts (timer with a code floor,
// finished or stalled directions, budget share, new evidence, a parked finish), opens at most one epoch at a time,
// calls the judge once per epoch, records the judgment (or a bad-judgment epoch that changes nothing else) and applies
// keep, prune and boost verdicts as direction state moves.
//
// It is a plain library: no init, no goroutine, no registration, nothing wired into a binary. The caller's host loop
// calls Tick; every effect goes through the narrow interfaces declared here. The engine never decides a finish: the
// result only reports what the judge thought, and the finish package alone decides.
package epoch

import (
	"sync"
	"time"
)

// Clock is the engine's only source of time.
type Clock interface{ Now() time.Time }

// RealClock reads the system clock.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a manually advanced clock for tests of this and neighbouring packages.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock starts at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }

// Now returns the fake time.
func (c *FakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance moves the fake time forward by d.
func (c *FakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
