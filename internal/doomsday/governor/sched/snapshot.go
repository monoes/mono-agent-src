package sched

import (
	"context"
	"sort"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Snapshot implements governor.Governor: numbers and reason names only, bounded (at most MaxProviders providers), no
// payloads and no member ids. It reads the cached decision when it is fresh and never waits for the sampler.
func (s *Scheduler) Snapshot(ctx context.Context) (governor.Snapshot, error) {
	eff := s.Effective(ctx)
	snap := governor.Snapshot{
		Cap: eff.Cap, Effective: eff.Value, Admitting: eff.Admitting && !s.stopped.Load(),
		Reasons: eff.Reasons, QueueMaxRows: s.l.QueueMaxRows, QueueMaxBytes: s.l.QueueMaxBytes,
	}
	s.sampleMu.Lock()
	snap.LastSample = s.lastSample
	s.sampleMu.Unlock()
	var err error
	if snap.LiveProcess, err = s.cfg.Slots.Live(ctx, s.l.Profile, s.l.Generation, governor.KindProcess, ""); err != nil {
		return snap, err
	}
	if snap.LiveSession, err = s.cfg.Slots.Live(ctx, s.l.Profile, s.l.Generation, governor.KindSession, ""); err != nil {
		return snap, err
	}
	if snap.QueueRows, snap.QueueBytes, err = s.cfg.Queue.Depth(ctx, s.l.Profile, s.l.Generation); err != nil {
		return snap, err
	}
	snap.Providers = s.providerSnapshots(s.cfg.Clock.Now())
	return snap, nil
}

func (s *Scheduler) providerSnapshots(now time.Duration) []governor.ProviderSnapshot {
	s.admitMu.Lock()
	names := make([]string, 0, len(s.seen))
	for p := range s.seen {
		names = append(names, p)
	}
	s.admitMu.Unlock()
	sort.Strings(names)
	var out []governor.ProviderSnapshot
	for _, p := range names {
		pe := s.cfg.Policy.Provider(s.l, p)
		if pe.Value < s.l.MaxConcurrent && pe.Until > now {
			out = append(out, governor.ProviderSnapshot{Provider: p, Value: pe.Value, Until: pe.Until})
		}
	}
	return out
}
