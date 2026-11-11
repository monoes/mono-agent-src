package coderhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// The start and the end of the goal checks in Host.Run.

// checkShutdownWait bounds how long Host.Run waits for the checks to end after the host's context is cancelled: the confiner's
// release (the unit's kill) must run, and a check that cannot finish stays running in its row until the next start sweeps it.
const checkShutdownWait = 45 * time.Second

// confinerHousekeeping is what a confiner that owns units outside this process offers (sdconfine does): end the units of dead
// hosts, unset stale variables it planted in the manager, and end this process's own units.
type confinerHousekeeping interface {
	Sweep(context.Context) ([]string, error)
	SweepEnv(context.Context) ([]string, error)
	Cleanup(context.Context) ([]string, error)
}

// pursuitCheckStart runs once, after Recover and before the first pass. A check still running in a row was started by a host
// that is gone (or by one that began before this one), so it is ended as abandoned, at the cap; only checks that started
// before THIS host started are touched, so a check this host (or a host that began earlier) runs is never swept from under it.
// With the checks set up it then clears what an earlier host left outside the database: first the confiner's units (a unit may
// still be using a scratch folder), then the scratch folders.
func (h *Host) pursuitCheckStart(ctx context.Context) {
	h.hostStart = h.Store.now()
	if !h.Pursuit.ready() {
		return
	}
	hc := missionpursuit.NewHostChecks(missionpursuit.NewStore(h.Store.db))
	if n, err := hc.AbandonRunning(ctx, h.hostStart); err != nil {
		h.logf("coder host: goal checks: the sweep of checks left running: %v", err)
	} else if n > 0 {
		h.logf("coder host: goal checks: %d check(s) left running by an earlier host were ended as abandoned (counted at the cap)", n)
	}
	if !h.verifierReady() {
		return
	}
	v := h.Pursuit.Verifier
	if hk, ok := v.Confiner.(confinerHousekeeping); ok {
		if units, err := hk.Sweep(ctx); err != nil {
			h.logf("coder host: goal checks: the sweep of a dead host's units: %v", err)
		} else if len(units) > 0 {
			h.logf("coder host: goal checks: ended %d unit(s) of a dead host", len(units))
		}
		if _, err := hk.SweepEnv(ctx); err != nil {
			h.logf("coder host: goal checks: the sweep of the manager's variables: %v", err)
		}
	}
	if v.Scratch != "" {
		sweepScratch(v.Scratch, h.logf)
	}
	if why := h.checkerBroken(); why != "" {
		h.logf("coder host: %s", why)
	}
}

// sweepScratch removes the check-* and crit-* folders an earlier host left in the scratch folder (never a link, never a file).
func sweepScratch(dir string, logf func(string, ...any)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !(strings.HasPrefix(name, "check-") || strings.HasPrefix(name, "crit-")) || e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		if err := finish.RemoveTree(filepath.Join(dir, name)); err != nil {
			logf("coder host: goal checks: a leftover scratch folder could not be removed: %v", err)
		}
	}
}

// pursuitCheckStop runs when Run ends (after the pass has): the checks were cancelled with the host's context; it waits for
// their goroutines, bounded, and ends the confiner's own units.
func (h *Host) pursuitCheckStop() {
	done := make(chan struct{})
	go func() { h.checkWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(checkShutdownWait):
		h.logf("coder host: goal checks: a check did not end within %s of the host stopping; its row is swept at the next start", checkShutdownWait)
	}
	if !h.verifierReady() {
		return
	}
	if hk, ok := h.Pursuit.Verifier.Confiner.(confinerHousekeeping); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := hk.Cleanup(ctx); err != nil {
			h.logf("coder host: goal checks: ending the confiner's units: %v", err)
		}
	}
}
