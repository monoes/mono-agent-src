// Package sched is the fair scheduler of the resource governor of the open-ended ("doomsday") mode. It implements
// governor.Governor over the injected Sampler, Policy, Slots, Queue, RunView and Clock, and adds Pump: one admission
// step that the host loop calls (there is no goroutine, no init() and no registration in this package).
//
// Rules it holds:
//   - a slot is reserved only while the decision is Admitting and the live process count is under Effective.Value;
//     the scheduler only ever LOWERS concurrency below the charter cap, it never raises it and never touches a
//     running member (no SIGSTOP, no kill);
//   - Stop is an atomic flag first: nothing is admitted after it and it never waits for the sampler, the policy or
//     an admission in flight;
//   - the machine is sampled at most once per SampleEvery and a sample in progress never blocks another caller.
package sched

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Defaults of Config.
const (
	DefaultSampleEvery   = time.Second
	DefaultSampleTimeout = 2 * time.Second
	DefaultPeekPage      = 64
	DefaultCancelTimeout = 5 * time.Second
	// MaxProviders bounds the provider names the scheduler remembers for its snapshot.
	MaxProviders = 64
)

// ErrStopped is the refusal after Stop.
var ErrStopped = governor.ErrNotRunning

// Decoder turns a queued item into the launch it stands for. Payload holds references only; a decoder error leaves
// the item queued and counts it in PumpResult.Skipped.
type Decoder func(governor.Item) (provider string, r governor.Request, err error)

// Config is everything the scheduler needs; there is no default for the injected parts.
type Config struct {
	Limits  governor.Limits
	Sampler governor.Sampler
	Policy  governor.Policy
	Slots   governor.Slots
	Queue   governor.Queue
	View    governor.RunView
	Clock   governor.Clock
	Decode  Decoder

	// Wall stamps Snapshot.LastSample (default time.Now); it never feeds a decision.
	Wall          func() time.Time
	SampleEvery   time.Duration
	SampleTimeout time.Duration
	PeekPage      int
	CancelTimeout time.Duration
}

// Admitted is one launch Pump reserved slots for; the host starts it and calls Release when it ends (or fails to start).
type Admitted struct {
	Item     governor.Item
	Provider string
	Request  governor.Request
}

// Scheduler implements governor.Governor.
type Scheduler struct {
	cfg     Config
	l       governor.Limits
	stopped atomic.Bool
	wake    chan struct{}

	admitMu sync.Mutex // serialises admissions (provider counts and the reserve); never held by Stop
	byMem   map[string]string
	perProv map[string]int
	seen    map[string]struct{}

	sampleMu   sync.Mutex // guards the cached decision
	sampling   atomic.Bool
	cached     governor.Effective
	cachedAt   time.Duration
	haveCached bool
	lastSample time.Time
}

var _ governor.Governor = (*Scheduler)(nil)

// New builds a scheduler for one run. It starts nothing.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Sampler == nil || cfg.Policy == nil || cfg.Slots == nil || cfg.Queue == nil || cfg.View == nil || cfg.Clock == nil {
		return nil, errors.New("doomsday governor sched: sampler, policy, slots, queue, run view and clock are required")
	}
	if cfg.Limits.MaxConcurrent < 1 {
		return nil, errors.New("doomsday governor sched: the charter cap must be at least 1")
	}
	if cfg.Wall == nil {
		cfg.Wall = time.Now
	}
	if cfg.SampleEvery <= 0 {
		cfg.SampleEvery = DefaultSampleEvery
	}
	if cfg.SampleTimeout <= 0 {
		cfg.SampleTimeout = DefaultSampleTimeout
	}
	if cfg.PeekPage <= 0 || cfg.PeekPage > DefaultPeekPage {
		cfg.PeekPage = DefaultPeekPage
	}
	if cfg.CancelTimeout <= 0 {
		cfg.CancelTimeout = DefaultCancelTimeout
	}
	return &Scheduler{
		cfg: cfg, l: cfg.Limits.WithDefaults(), wake: make(chan struct{}, 1),
		byMem: map[string]string{}, perProv: map[string]int{}, seen: map[string]struct{}{},
	}, nil
}

// Wake is signalled (never blocking the sender) when a slot is released, a launch is queued or a rate limit is
// reported; the host loop selects on it together with its own timer and calls Pump.
func (s *Scheduler) Wake() <-chan struct{} { return s.wake }

func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Effective implements governor.Governor. It reuses the last decision for SampleEvery; when a refresh is already in
// progress the caller gets the last decision (or the safe minimum before the first one) instead of waiting.
func (s *Scheduler) Effective(ctx context.Context) governor.Effective {
	now := s.cfg.Clock.Now()
	s.sampleMu.Lock()
	if s.haveCached && now >= s.cachedAt && now-s.cachedAt < s.cfg.SampleEvery {
		e := cloneEffective(s.cached)
		s.sampleMu.Unlock()
		return e
	}
	if !s.sampling.CompareAndSwap(false, true) {
		e := s.fallbackLocked()
		s.sampleMu.Unlock()
		return e
	}
	s.sampleMu.Unlock()
	e := s.refresh(ctx, now)
	return cloneEffective(e)
}

func (s *Scheduler) fallbackLocked() governor.Effective {
	if s.haveCached {
		return cloneEffective(s.cached)
	}
	return governor.Effective{Cap: s.l.MaxConcurrent, Value: s.l.SafeMin, Admitting: true, Reasons: []governor.Reason{governor.ReasonSampleFail}}
}

func (s *Scheduler) refresh(ctx context.Context, now time.Duration) governor.Effective {
	defer s.sampling.Store(false)
	sctx, cancel := context.WithTimeout(ctx, s.cfg.SampleTimeout)
	defer cancel()
	smp, err := s.readSample(sctx)
	e := s.cfg.Policy.Decide(s.l, smp, err)
	// the charter cap is the ceiling whatever a policy says
	if e.Value > s.l.MaxConcurrent {
		e.Value = s.l.MaxConcurrent
	}
	s.sampleMu.Lock()
	s.cached, s.cachedAt, s.haveCached = cloneEffective(e), now, true
	s.lastSample = s.cfg.Wall()
	s.sampleMu.Unlock()
	return e
}

func (s *Scheduler) readSample(ctx context.Context) (smp governor.Sample, err error) {
	defer func() {
		if r := recover(); r != nil {
			smp, err = governor.Sample{}, errors.New("doomsday governor sched: the sampler panicked")
		}
	}()
	return s.cfg.Sampler.Sample(ctx)
}

func cloneEffective(e governor.Effective) governor.Effective {
	e.Reasons = append([]governor.Reason(nil), e.Reasons...)
	return e
}

// TryAdmit implements governor.Governor.
func (s *Scheduler) TryAdmit(ctx context.Context, provider string, r governor.Request) error {
	if s.stopped.Load() {
		return ErrStopped
	}
	eff := s.Effective(ctx)
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	return s.admitLocked(ctx, eff, provider, r)
}

// admitLocked reserves slots for r against the decision eff. admitMu is held.
func (s *Scheduler) admitLocked(ctx context.Context, eff governor.Effective, provider string, r governor.Request) error {
	if s.stopped.Load() {
		return ErrStopped
	}
	if !eff.Admitting {
		return governor.ErrNoSlot
	}
	limit := eff.Value
	if limit < 1 {
		return governor.ErrNoSlot
	}
	if provider != "" && s.providerFull(provider) {
		return governor.ErrNoSlot
	}
	live, err := s.cfg.Slots.Live(ctx, s.l.Profile, s.l.Generation, governor.KindProcess, "")
	if err != nil {
		return err
	}
	if live >= limit {
		return governor.ErrNoSlot
	}
	if err := s.cfg.Slots.Reserve(ctx, s.l, limit, r); err != nil {
		return err
	}
	if s.stopped.Load() { // a Stop that landed during the reserve wins
		s.releaseQuiet(r.MemberID)
		return ErrStopped
	}
	s.track(provider, r.MemberID)
	return nil
}

func (s *Scheduler) providerFull(provider string) bool {
	pe := s.cfg.Policy.Provider(s.l, provider)
	if pe.Value >= s.l.MaxConcurrent {
		return false
	}
	return s.perProv[provider] >= pe.Value
}

func (s *Scheduler) track(provider, member string) {
	if provider == "" {
		return
	}
	s.byMem[member] = provider
	s.perProv[provider]++
	if _, ok := s.seen[provider]; !ok && len(s.seen) < MaxProviders {
		s.seen[provider] = struct{}{}
	}
}

// releaseQuiet frees a member's slots on a path that must not fail the caller; the context is detached so a
// cancelled caller cannot leak a slot.
func (s *Scheduler) releaseQuiet(member string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), s.cfg.CancelTimeout)
	defer cancel()
	_ = s.cfg.Slots.Release(ctx, s.l.Profile, s.l.Generation, member)
}

// Enqueue implements governor.Governor.
func (s *Scheduler) Enqueue(ctx context.Context, it governor.Item) (int64, error) {
	if s.stopped.Load() {
		return 0, ErrStopped
	}
	id, err := s.cfg.Queue.Enqueue(ctx, s.l, it)
	if err == nil {
		s.signal()
	}
	return id, err
}

// ReportRateLimit implements governor.Governor.
func (s *Scheduler) ReportRateLimit(provider string, retryAfter time.Duration) {
	s.cfg.Policy.RateLimited(provider, retryAfter)
	s.admitMu.Lock()
	if _, ok := s.seen[provider]; !ok && len(s.seen) < MaxProviders {
		s.seen[provider] = struct{}{}
	}
	s.admitMu.Unlock()
	s.signal()
}

// Release implements governor.Governor: it frees the member's slots and wakes the scheduler. It works after Stop.
func (s *Scheduler) Release(ctx context.Context, memberID string) error {
	err := s.cfg.Slots.Release(ctx, s.l.Profile, s.l.Generation, memberID)
	s.admitMu.Lock()
	if p, ok := s.byMem[memberID]; ok {
		delete(s.byMem, memberID)
		if s.perProv[p]--; s.perProv[p] <= 0 {
			delete(s.perProv, p)
		}
	}
	s.admitMu.Unlock()
	s.signal()
	return err
}

// NotifyStop marks the scheduler stopped. It is one atomic store: it takes no lock, does no I/O and cannot block.
func (s *Scheduler) NotifyStop() { s.stopped.Store(true) }

// Stopped reports whether Stop or NotifyStop ran.
func (s *Scheduler) Stopped() bool { return s.stopped.Load() }

// Stop makes the scheduler refuse everything from now on, then cancels the queue (a stopped run's queue never
// resumes) within CancelTimeout. The flag is set before any I/O, so even a wedged database cannot make an admission
// succeed after Stop was called. Running members are not touched.
func (s *Scheduler) Stop(ctx context.Context) (cancelled int, err error) {
	s.NotifyStop()
	cctx, cancel := context.WithTimeout(ctx, s.cfg.CancelTimeout)
	defer cancel()
	return s.cfg.Queue.CancelAll(cctx, s.l.Profile, s.l.Generation)
}
