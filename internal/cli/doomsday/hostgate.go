package doomsday

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// HostGate is the coder host's dispatch gate in an engine built with the mode. It is a coderhost.DispatchGate that
// looks at the profile's open run on every dispatch:
//
//   - no open run: the call is made plainly, exactly as a host without the mode does it;
//   - an open run that is running (and whose stop marker is absent): the dispatch takes a ticket from the
//     generation's stop.Gate (admit, fence, register);
//   - an open run that is stop-requested or later, a stop marker, or a run state that cannot be read: the gate of
//     that generation is sealed and the dispatch is refused with coderhost.ErrDispatchRefused, nothing started.
//
// The host is a different process from `doomsday stop`, so Stop reaches this gate through the marker and the
// database intent, which are read on every dispatch; there is no in-process call to rely on.
type HostGate struct {
	Store   *store.SQLStore
	Profile string
	RunDir  stop.RunDir
	Clock   stop.Clock
	// Kernel is the cgroup seam the merge's git commands are placed through (stop.LinuxKernel); nil refuses every
	// merge while a run is open (it could not be placed in the unit).
	Kernel stop.Kernel
	// Journal is the merge journal (MergeFor); nil refuses every merge while a run is open.
	Journal dynorg.MergeJournal
	// BindingOn says whether security.peercred_binding is on. A run needs bound turns ("no run without binding"): nil, an
	// error and false all refuse a turn start while a run is open.
	BindingOn func() (bool, error)
	// InstallHooks puts Hooks on the host's launchers (chat.InstallRunHooks in production). Install reports its absence.
	InstallHooks func(st *coderhost.Store, h *coderhost.Host, hk Hooks) error

	mu       sync.Mutex
	gates    map[int64]*stop.Gate
	merges   map[int64]*dynorg.DoomsdayMerge
	hooked   bool            // the launcher asks BeforeRelease: a turn's ticket is taken there, not around Start
	released map[string]bool // turn ids that already hold their ticket
}

var _ coderhost.DispatchGate = (*HostGate)(nil)

// Install puts the gate on the coder host: the store's own dispatches (spawn, token mint) and the launcher's turn
// starts. Call it once, on the host being built, before it runs.
//
// The hooks go on the launchers first (the run stamp and the release check, where a turn's ticket is taken), then the
// launcher is wrapped (the bound-mode refusal before Start). When the hooks cannot be installed the error is returned and
// the gate still stands, with the ticket taken around Start instead: the host never runs ungated because of it.
func (g *HostGate) Install(st *coderhost.Store, h *coderhost.Host) error {
	st.SetDispatchGate(g)
	err := errors.New("no hook installer is wired")
	if g.InstallHooks != nil {
		err = g.InstallHooks(st, h, Hooks{RunStamp: g.RunStamp, BeforeRelease: g.BeforeRelease, Place: g.PlaceRelease})
	}
	g.mu.Lock()
	g.hooked = err == nil
	g.mu.Unlock()
	h.L = coderhost.WrapLauncher(h.L, g)
	return err
}

// current returns the gate of the profile's open run, or nil when no run is open. A refusal error means the
// dispatch must not happen.
func (g *HostGate) current(ctx context.Context) (*stop.Gate, error) {
	gate, _, err := g.currentRun(ctx)
	return gate, err
}

// currentRun is current with the run the gate belongs to.
func (g *HostGate) currentRun(ctx context.Context) (gate *stop.Gate, r store.Run, err error) {
	runs, err := g.Store.OpenRuns(ctx, g.Profile)
	if err != nil {
		return nil, r, fmt.Errorf("%w: the run state cannot be read: %v", coderhost.ErrDispatchRefused, err)
	}
	if len(runs) == 0 {
		return nil, r, nil
	}
	r = runs[0]
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = map[int64]*stop.Gate{}
	}
	gate = g.gates[r.Generation]
	if gate == nil {
		clk := g.Clock
		if clk == nil {
			clk = stop.SystemClock{}
		}
		gate = stop.NewGate(stop.GateConfig{Generation: uint64(r.Generation), Reg: auditRegistry{g.Store, g.Profile, r.Generation}, Clock: clk})
		g.gates[r.Generation] = gate
	}
	if r.State != store.StateRunning || g.RunDir.ReadMarker(uint64(r.Generation), r.CharterID).Stops() {
		gate.Seal()
	}
	return gate, r, nil
}

// Hand implements coderhost.DispatchGate.
func (g *HostGate) Hand(p coderhost.DispatchPoint, fn func(ctx context.Context) error) error {
	gate, err := g.current(context.Background())
	if err != nil {
		return err
	}
	if gate == nil {
		return fn(context.Background())
	}
	return coderhost.StopGate{G: gate}.Hand(p, fn)
}

// Start implements coderhost.DispatchGate.
func (g *HostGate) Start(p coderhost.DispatchPoint, create func() (coderhost.StartedProc, error)) error {
	gate, err := g.current(context.Background())
	if err != nil {
		return err
	}
	if gate == nil {
		_, err := create()
		return err
	}
	if p == coderhost.PointTurnStart {
		if err := g.requireBound(); err != nil { // before Start: a turn that is not bound never gets a process
			return err
		}
		g.mu.Lock()
		hooked := g.hooked
		g.mu.Unlock()
		if hooked && !gate.Sealed() { // the launcher asks BeforeRelease, which takes the ticket; a sealed gate refuses below
			_, err := create()
			return err
		}
	}
	return coderhost.StopGate{G: gate}.Start(p, create)
}

// auditRegistry records the tickets of one generation in its MAC-chained audit stream. A ticket is not a member
// row: a turn's process is held by the containment unit, and a member row would need the launcher to release it
// when the turn ends, which this change does not wire.
type auditRegistry struct {
	st  *store.SQLStore
	pro string
	gen int64
}

func (r auditRegistry) append(kind, payload string) error {
	var err error
	for i := 0; i < 5; i++ { // two writers can race for the same seq; the loser retries
		if _, err = r.st.AppendAudit(context.Background(), r.pro, r.gen, kind, payload); err == nil {
			return nil
		}
	}
	return err
}

func (r auditRegistry) RecordStarted(t stop.TicketInfo, handle string) error {
	return r.append("ticket.started", fmt.Sprintf("%d %s %s", t.ID, t.Op, handle))
}

func (r auditRegistry) RecordAborted(t stop.TicketInfo, approvalID string) error {
	if approvalID != "" {
		return r.append("aborted-after-consume", fmt.Sprintf("%d %s approval=%s", t.ID, t.Op, approvalID))
	}
	return r.append("ticket.aborted", fmt.Sprintf("%d %s", t.ID, t.Op))
}

func (r auditRegistry) RecordUnknown(t stop.TicketInfo) error {
	return r.append("ticket.unknown", fmt.Sprintf("%d %s", t.ID, t.Op))
}
