package sched

import (
	"context"
	"errors"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// PumpResult is what one Pump step did.
type PumpResult struct {
	Admitted []Admitted
	Skipped  int // items the decoder could not read; they stay queued
	Deferred int // items left queued because a slot, the provider or the tree had no room
}

// Pump admits as many queued launches as free slots allow, in service order (boss class first, then leads round
// robin, FIFO within a lead). The host loop calls it on Wake and on its timer; it starts no goroutine.
//
// Order of effects per item: Reserve the slots first, MarkAdmitted second, release the slots if MarkAdmitted fails.
// A crash between the two leaves the item queued (no lost work) and the slot an orphan that Slots.Reconcile frees
// (no leaked slot). Nothing is admitted after Stop, while the run is not running, or while the decision is not
// admitting. An item whose provider is backing off is passed over so it cannot hold up other providers.
func (s *Scheduler) Pump(ctx context.Context) (PumpResult, error) {
	var res PumpResult
	if s.stopped.Load() {
		return res, ErrStopped
	}
	running, err := s.cfg.View.Running(ctx, s.l.Profile, s.l.Generation)
	if err != nil {
		return res, err
	}
	if !running {
		return res, governor.ErrNotRunning
	}
	eff := s.Effective(ctx)
	if !eff.Admitting || eff.Value < 1 {
		return res, nil
	}
	items, err := s.cfg.Queue.Peek(ctx, s.l.Profile, s.l.Generation, s.cfg.PeekPage)
	if err != nil {
		return res, err
	}
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	for _, it := range items {
		if s.stopped.Load() {
			return res, ErrStopped
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		provider, req, derr := s.decode(it)
		if derr != nil {
			res.Skipped++
			continue
		}
		err := s.admitLocked(ctx, eff, provider, req)
		switch {
		case err == nil:
		case errors.Is(err, governor.ErrNoSlot):
			res.Deferred++
			if s.processFull(ctx, eff) {
				return res, nil
			}
			continue
		default:
			return res, err
		}
		if merr := s.cfg.Queue.MarkAdmitted(ctx, s.l.Profile, s.l.Generation, it.QID); merr != nil {
			s.undo(provider, req.MemberID)
			if errors.Is(merr, governor.ErrNotQueued) {
				continue // cancelled or taken meanwhile: nothing to run, nothing leaked
			}
			return res, merr
		}
		res.Admitted = append(res.Admitted, Admitted{Item: it, Provider: provider, Request: req})
	}
	return res, nil
}

func (s *Scheduler) decode(it governor.Item) (provider string, r governor.Request, err error) {
	if s.cfg.Decode == nil {
		return "", governor.Request{}, errors.New("doomsday governor sched: no decoder")
	}
	return s.cfg.Decode(it)
}

// undo gives back what a failed MarkAdmitted reserved.
func (s *Scheduler) undo(provider, member string) {
	s.releaseQuiet(member)
	if provider == "" {
		return
	}
	delete(s.byMem, member)
	if s.perProv[provider]--; s.perProv[provider] <= 0 {
		delete(s.perProv, provider)
	}
}

// processFull reports whether the process slots are all taken, so the rest of the page cannot fit either.
func (s *Scheduler) processFull(ctx context.Context, eff governor.Effective) bool {
	live, err := s.cfg.Slots.Live(ctx, s.l.Profile, s.l.Generation, governor.KindProcess, "")
	return err != nil || live >= eff.Value
}
