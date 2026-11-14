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
	// Adopt puts the already started turn's process into the run's unit (HostGate.Adopt); held says the unit reaps the leader.
	Adopt func(profileID, conversationID string, pid int, stamp string) (held bool, err error)
	// Place puts the turn's process in the run's ticket cgroup before it exists; release closes the descriptor after Start.
	Place func(cmd *exec.Cmd, profileID, conversationID string) (release func(), err error)
	// PlaceRequired says whether a turn must be placed (a run is open); the legacy launch is refused only then.
	PlaceRequired func(profileID, conversationID string) (bool, error)
}

const maxReleased = 4096

// noteStart records the generation of the open run (0: none) that Place or Adopt saw when conv's turn was started; takeStart reads and
// consumes it at BeforeRelease. A launcher that never asked Place leaves no record, and a record the release never consumed (a start that
// failed) is replaced by the conversation's next start.
func (g *HostGate) noteStart(conv string, gen int64) {
	g.holdMu.Lock()
	defer g.holdMu.Unlock()
	if g.started == nil || len(g.started) >= maxReleased {
		g.started = map[string]int64{}
	}
	g.started[conv] = gen
}

func (g *HostGate) takeStart(conv string) (int64, bool) {
	g.holdMu.Lock()
	defer g.holdMu.Unlock()
	gen, ok := g.started[conv]
	delete(g.started, conv)
	return gen, ok
}

// releaseCall is the one decision for a turn id: the first call takes the ticket and publishes its outcome on done; a
// concurrent repeat waits for that outcome and returns it, so it can never be allowed ahead of a first call that still fails.
type releaseCall struct {
	done chan struct{}
	err  error
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{coderhost.ErrDispatchRefused}, a...)...)
}

// requireBound is "no run without binding": while a run is open a turn that is not peer-credential bound is not started.
// An unreadable setting is not "on".
func (g *HostGate) requireBound(generation int64) error {
	if g.BindingOn == nil {
		return refuse("no run without binding: whether turn binding is on is not known")
	}
	on, err := g.BindingOn()
	if err != nil {
		return refuse("no run without binding: whether turn binding is on cannot be read: %v", err)
	}
	if !on && !g.unboundAllowed(generation) {
		return refuse("no run without binding: this run started with peer-credential binding and it is not available now; `doomsday stop` ends the run if this keeps failing")
	}
	return nil
}

// unboundAllowed is true only for a run that started unbound (its own run.unbound row): the allowance belongs to the run, not to
// whatever the machine check says now.
func (g *HostGate) unboundAllowed(generation int64) bool {
	return g.UnboundRun != nil && g.UnboundRun(context.Background(), g.Profile, generation)
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
func (g *HostGate) BeforeRelease(ctx context.Context, _, conv, turnID string, bound bool) error {
	gate, run, err := g.currentRun(context.Background()) // not ctx: the only cancellation check is the one under the lock
	startGen, started := g.takeStart(conv)
	if err != nil {
		return err
	}
	if gate == nil {
		return nil
	}
	if !bound && !g.unboundAllowed(run.Generation) {
		return refuse("no run without binding: this turn is not peer-credential bound, and this run did not start unbound; `doomsday stop` ends the run if this keeps failing")
	}
	if started && startGen != run.Generation {
		// Place and Adopt looked at the open run when the turn was started: it was another run, or none. A turn started outside this run's
		// unit is not released into it.
		return refuse("the run (generation %d) opened after this turn was started (it saw generation %d), so the turn was not placed in its unit", run.Generation, startGen)
	}
	// The gate's own lock covers the cancellation check and the in-memory ticket steps only (Admit, BeginStart): the registry
	// write of Started (an audit append with retries and an fsync) happens after it is released, so one slow write never queues
	// every dispatch behind it.
	g.mu.Lock()
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}
	if c := g.released[turnID]; c != nil {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &releaseCall{done: make(chan struct{})}
	if g.released == nil || len(g.released) >= maxReleased {
		g.released = map[string]*releaseCall{} // a call in flight keeps its outcome: its repeaters hold the pointer
	}
	g.released[turnID] = call
	t, err := gate.Admit(stop.Spec{Kind: stop.KindE1, Op: string(coderhost.PointTurnStart)})
	if err == nil {
		err = gate.BeginStart(t)
	}
	g.mu.Unlock()
	if err == nil {
		err = gate.Started(t, "turn:"+turnID)
	}
	if err != nil {
		if t != nil { // Admit refused: there is no ticket to abort
			gate.Abort(t)
		}
		err = refuse("%v", err)
		g.mu.Lock()
		if g.released[turnID] == call {
			delete(g.released, turnID) // a refused turn holds nothing: a later attempt is a fresh decision
		}
		g.mu.Unlock()
	}
	call.err = err
	close(call.done)
	return err
}
