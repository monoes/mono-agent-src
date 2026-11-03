package doomsday

import (
	"context"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// reachLocked makes the generation's gate reachable by a Stop in another process (note 13.1 section 7, issue #1269):
//
//   - the host lock of the run directory is taken once and held for the process's life (a second host is refused);
//   - the emergency socket is bound (a Stop sends its message there; a bind error refuses, because a Stop would read a
//     missing socket as "no host");
//   - a watcher polls the marker and the run state every 250 ms, closes the gate on a stop, writes the acknowledgement the
//     Stop waits for, and enforces the crash-only exit for a start call that never returns.
//
// Called with g.mu held, once per generation.
func (g *HostGate) reachLocked(gate *stop.Gate, r store.Run, clk stop.Clock) error {
	if !g.lockHeld {
		lock := g.HostLock
		if lock == nil {
			lock = stop.AcquireHost
		}
		if _, err := lock(g.RunDir); err != nil { // never released: the host holds it until it exits
			return fmt.Errorf("the host lock is not available: %w", err)
		}
		g.lockHeld = true
	}
	gen := uint64(r.Generation)
	w := stop.NewHostWatcher(&stop.HostWatcher{Gate: gate, Dir: g.RunDir, Gen: gen, CharterID: r.CharterID, Clock: clk, Tick: g.Tick,
		Stops: func(ctx context.Context) bool { return g.runStopped(ctx, r.Generation) }, Exit: g.exit})
	srv := &stop.EmergencyServer{Dir: g.RunDir, Gen: gen, UID: uint32(os.Getuid()), OnStop: func(uint64) { w.Kick() }}
	if err := srv.Start(context.Background()); err != nil {
		return fmt.Errorf("the emergency channel cannot be bound: %w", err)
	}
	go w.Run(context.Background())
	return nil
}

// runStopped says the generation is no longer running: its state moved on, it is not open any more, (a state that cannot be read is not a stop here).
func (g *HostGate) runStopped(ctx context.Context, gen int64) bool {
	runs, err := g.Store.OpenRuns(ctx, g.Profile)
	if err != nil {
		return false // a read that fails seals nothing for good: every dispatch already refuses while the state cannot be read
	}
	for _, r := range runs {
		if r.Generation == gen {
			return r.State != store.StateRunning
		}
	}
	return true
}

func (g *HostGate) exit(starters []uint64) {
	if g.Exit != nil {
		g.Exit(starters)
		return
	}
	fmt.Fprintf(os.Stderr, "doomsday: a start call did not return within T_hung (tickets %v); the host exits (crash-only)\n", starters)
	os.Exit(70)
}
