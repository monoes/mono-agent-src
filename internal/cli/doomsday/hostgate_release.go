package doomsday

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// Hooks are what an open run installs into the host's launchers (chat.RunHooks, in the types this package can name).
type Hooks struct {
	// RunStamp says which run (charter id, generation) a session's turn belongs to; "", 0 stamps nothing.
	RunStamp func(profileID, conversationID string) (charterID string, generation int64)
	// BeforeRelease is asked before anything is delivered to the turn's process; bound says whether the turn is bound.
	BeforeRelease func(ctx context.Context, profileID, conversationID, turnID string, bound bool) error
	// Place puts the turn's process in the run's ticket cgroup before it exists; release closes the descriptor after Start.
	Place func(cmd *exec.Cmd, profileID, conversationID string) (release func(), err error)
}

const maxReleased = 4096

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{coderhost.ErrDispatchRefused}, a...)...)
}

// requireBound is "no run without binding": while a run is open a turn that is not peer-credential bound is not started.
// An unreadable setting is not "on".
func (g *HostGate) requireBound() error {
	if g.BindingOn == nil {
		return refuse("no run without binding: whether turn binding is on is not known")
	}
	on, err := g.BindingOn()
	if err != nil {
		return refuse("no run without binding: whether turn binding is on cannot be read: %v", err)
	}
	if !on {
		return refuse("no run without binding: peer-credential binding is off; switch it on (coder set --peercred-binding on)")
	}
	return nil
}

// RunStamp is the supplier of the run stamp: the profile's open run, or nothing (a zero answer stamps nothing, so a host with
// no open run is what it was). A run state that cannot be read stamps nothing; BeforeRelease refuses that turn anyway.
func (g *HostGate) RunStamp(string, string) (string, int64) {
	runs, err := g.Store.OpenRuns(context.Background(), g.Profile)
	if err != nil || len(runs) == 0 {
		return "", 0
	}
	return runs[0].CharterID, runs[0].Generation
}

// BeforeRelease is the launcher's hook (chat.RunHooks): with an open run, a turn that is not bound is refused, and a bound
// one takes its ticket here, after the process exists and before anything is delivered to it.
//
// The ticket is taken under the gate's own lock, after ctx.Err() is checked under that same lock: a check that finishes after
// the launcher gave up (its deadline cancelled ctx) consumes nothing. A turn id holds at most one ticket, so a repeated call
// for it (a retry, a late duplicate) consumes nothing more.
func (g *HostGate) BeforeRelease(ctx context.Context, _, _, turnID string, bound bool) error {
	gate, err := g.current(context.Background()) // not ctx: the only cancellation check is the one under the lock
	if err != nil {
		return err
	}
	if gate == nil {
		return nil
	}
	if !bound {
		return refuse("no run without binding: this turn is not peer-credential bound")
	}
	// The gate's own lock covers the cancellation check and the in-memory ticket steps only (Admit, BeginStart): the registry
	// write of Started (an audit append with retries and an fsync) happens after it is released, so one slow write never queues
	// every dispatch behind it.
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	if g.released[turnID] {
		g.mu.Unlock()
		return nil
	}
	t, err := gate.Admit(stop.Spec{Kind: stop.KindE1, Op: string(coderhost.PointTurnStart)})
	if err == nil {
		err = gate.BeginStart(t)
	}
	if err == nil {
		if g.released == nil || len(g.released) >= maxReleased {
			g.released = map[string]bool{}
		}
		g.released[turnID] = true // a repeat for this turn consumes nothing, even while Started is still being written
	}
	g.mu.Unlock()
	if err == nil {
		err = gate.Started(t, "turn:"+turnID)
		if err != nil {
			g.mu.Lock()
			delete(g.released, turnID)
			g.mu.Unlock()
		}
	}
	if err != nil {
		gate.Abort(t)
		return refuse("%v", err)
	}
	return nil
}
