// Package policy is the pure core of the resource governor: effective concurrency = min(charter cap, load, memory,
// disk) with hysteresis, a recovery ramp and a floor, plus per-provider 429 backoff. It does no I/O and starts nothing;
// time comes from an injected monotonic governor.Clock.
//
// Invariants, for every input including NaN, negative, huge and missing values:
//
//	SafeMin <= Value <= Cap whenever Cap >= 1 (SafeMin is Limits.WithDefaults().SafeMin, 1..Cap);
//	Value == 0 and Admitting == false when Cap < 1 (nothing may start);
//	Value never rises by more than one step per RecoverySamples consecutive good samples;
//	a failed or implausible reading yields exactly SafeMin.
package policy

import (
	"math"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Plausibility bounds: a sample outside them counts as a failed reading.
const (
	maxLoad  = 1e6
	maxCores = 1 << 16
)

// Policy implements governor.Policy. Its zero value is not usable; use New.
type Policy struct {
	mu    sync.Mutex
	clock governor.Clock
	rand  func() float64
	st    state
	prov  providers
}

var _ governor.Policy = (*Policy)(nil)

// Option configures New.
type Option func(*Policy)

// WithJitter replaces the random source of the provider backoff jitter (a value in [0,1); anything else is 0).
func WithJitter(f func() float64) Option { return func(p *Policy) { p.rand = f } }

// New returns a Policy reading time from clock. A nil clock is replaced by a fixed clock at zero, which makes every
// backoff last forever: callers always pass a real clock (SystemClock).
func New(clock governor.Clock, opts ...Option) *Policy {
	if clock == nil {
		clock = fixedClock{}
	}
	p := &Policy{clock: clock, rand: func() float64 { return 0 }, prov: newProviders()}
	for _, o := range opts {
		o(p)
	}
	return p
}

type fixedClock struct{}

func (fixedClock) Now() time.Duration { return 0 }

// Decide implements governor.Policy.
func (p *Policy) Decide(l governor.Limits, s governor.Sample, sampleErr error) governor.Effective {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.MaxConcurrent < 1 {
		p.st = state{}
		return governor.Effective{Cap: 0, Value: 0, Admitting: false}
	}
	l = l.WithDefaults()
	capN, floor := l.MaxConcurrent, l.SafeMin
	if sampleErr != nil || !plausible(s) {
		p.st = state{init: true, level: floor}
		return governor.Effective{Cap: capN, Value: floor, Admitting: true, Reasons: []governor.Reason{governor.ReasonSampleFail}}
	}
	st := &p.st
	if !st.init {
		st.init, st.level = true, capN
	}
	st.level = clampInt(st.level, floor, capN)
	st.update(s)

	target, reasons := st.target(s, capN, floor)
	admitting := !st.memStop
	switch {
	case target < st.level:
		st.level, st.good = target, 0
	case target > st.level:
		st.good++
		if st.good >= RecoverySamples {
			st.level, st.good = st.level+1, 0
		}
	default:
		st.good = 0
	}
	if st.level < target {
		reasons = append(reasons, governor.ReasonRecovering)
	}
	return governor.Effective{Cap: capN, Value: st.level, Admitting: admitting, Reasons: reasons}
}

// plausible reports whether a sample may be acted on; anything else is a failed reading.
func plausible(s governor.Sample) bool {
	return s.Valid &&
		finite(s.Load1) && s.Load1 >= 0 && s.Load1 <= maxLoad &&
		s.Cores >= 1 && s.Cores <= maxCores &&
		finite(s.MemAvailFrac) && s.MemAvailFrac >= 0 && s.MemAvailFrac <= 1 &&
		finite(s.DiskFreeFrac) && s.DiskFreeFrac >= 0 && s.DiskFreeFrac <= 1
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
