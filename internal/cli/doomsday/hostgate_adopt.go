package doomsday

import (
	"context"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// Adopt is the launcher's adopt hook (chat.RunHooks.Adopt): the turn's process exists, is identified and has been given nothing; with a
// run open whose unit takes a process only after it exists (the macOS session unit, stop.Adopter) it is recorded in the unit here. Place
// and Adopt are a pair, and nothing runs between them: a refusal, or a run whose state cannot be read, fails the adoption and the launcher
// ends the child.
//
// held says the unit has taken over reaping the leader (its hold keeps the leader's number reserved until the unit lets go), which is
// also true of a refusal that came after the hold was taken (stop.ErrReapOwned): the launcher must not wait for the process then.
// With no run open, or a placement that takes the process at birth, nothing is adopted and the leader stays the launcher's own.
func (g *HostGate) Adopt(profile, conv string, pid int, stamp string) (held bool, err error) {
	pl, gen, err := g.placerRun(profile, conv)
	if err != nil {
		return false, err
	}
	if pl == nil {
		return false, nil
	}
	ad, ok := pl.(stop.Adopter)
	if !ok {
		return false, nil
	}
	err = ad.Adopt(pid, stamp)
	if err != nil {
		return errors.Is(err, stop.ErrReapOwned), err
	}
	if u, ok := pl.(interface{ UnitID() uint64 }); ok {
		g.watchHolds(gen, u.UnitID())
	}
	return true, nil
}

// watchHolds starts, once per unit, the watch that lets go of the unit's held leaders when its end line is written: the holds keep the
// numbers of leaders reserved while the unit is open (stop.ReleaseUnitHolds). The end is written by `doomsday stop`, another process,
// so the host looks for it; a witness that cannot be read is not an end.
func (g *HostGate) watchHolds(gen int64, unit uint64) {
	if unit == 0 {
		return
	}
	g.holdMu.Lock()
	defer g.holdMu.Unlock()
	if g.holdsDone {
		return
	}
	if _, on := g.holdWatch[unit]; on {
		return
	}
	if g.holdWatch == nil {
		g.holdWatch = map[uint64]context.CancelFunc{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.holdWatch[unit] = cancel
	if g.UnitEnded == nil {
		return // no way to learn of the end: the unit is still known, so shutdown releases it
	}
	poll := g.HoldPoll
	if poll <= 0 {
		poll = time.Second
	}
	go func() {
		tk := time.NewTicker(poll)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			if ended, err := g.UnitEnded(uint64(gen), unit); err != nil || !ended {
				continue
			}
			g.holdMu.Lock()
			_, still := g.holdWatch[unit]
			delete(g.holdWatch, unit)
			g.holdMu.Unlock()
			if still { // ReleaseAllHolds did not already take it
				g.releaseHolds(unit)
			}
			return
		}
	}()
}

func (g *HostGate) releaseHolds(unit uint64) {
	if g.ReleaseHolds != nil {
		g.ReleaseHolds(unit)
		return
	}
	stop.ReleaseUnitHolds(unit)
}

// ReleaseAllHolds is the host's shutdown: every unit it holds leaders for is let go of, ended or not (the process is ending and its
// leaders become another process's children), and the watches stop. Idempotent; after it no watch starts.
func (g *HostGate) ReleaseAllHolds() {
	g.holdMu.Lock()
	g.holdsDone = true
	units := make([]uint64, 0, len(g.holdWatch))
	for u, cancel := range g.holdWatch {
		cancel()
		units = append(units, u)
	}
	g.holdWatch = nil
	g.holdMu.Unlock()
	for _, u := range units {
		g.releaseHolds(u)
	}
	if g.ReleaseHolds == nil {
		stop.ReleaseAllUnitHolds() // also the holds no watch knows of
	}
}

// Seams (red commit): not used yet.
var (
	releaseUnitHolds    = stop.ReleaseUnitHolds
	releaseAllUnitHolds = stop.ReleaseAllUnitHolds
	holdPollMax         = 15 * time.Second
)
