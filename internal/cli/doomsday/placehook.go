package doomsday

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// PlaceHook is the placement hook of a launcher that starts a tool process for a coder turn: it is called with
// the command before Start. A nil error with no run open leaves cmd untouched; with a run open cmd starts BORN in a
// ticket cgroup of the run's containment unit (no hold stub is needed for it); an error means cmd must not be started.
// profile and conv name the turn's profile and conversation for the refusal text.
type PlaceHook func(cmd *exec.Cmd, profile, conv string) error

var _ PlaceHook = (*HostGate)(nil).PlaceCmd

// PlaceCmd is the PlaceHook of the host's gate. The ticket cgroup is released when cmd is garbage collected (the
// process outlives this call); Stop's EndUnit ends what is left. It reads the open run on every call, so a run
// that starts or stops after the host was built is seen.
func (g *HostGate) PlaceCmd(cmd *exec.Cmd, profile, conv string) error {
	pl, err := g.placer(profile, conv)
	if err != nil || pl == nil {
		return err
	}
	return pl.PlaceLeased(cmd)
}

// PlaceRelease is PlaceCmd for a launcher that closes the ticket cgroup's descriptor itself: release is called once
// the process exists (or failed to start) and is never nil on success. With no run open cmd is untouched and release
// does nothing; with a run open and a unit that cannot be placed in, the error means cmd must not be started.
// It is the shape chat.RunHooks.Place takes.
func (g *HostGate) PlaceRelease(cmd *exec.Cmd, profile, conv string) (release func(), err error) {
	pl, err := g.placer(profile, conv)
	if err != nil {
		return nil, err
	}
	if pl == nil {
		return func() {}, nil
	}
	return pl.Place(cmd)
}

// newPlacer is stop.NewPlacer; a seam so a test can hand the gate the placement of a unit kind the test host does not use.
var newPlacer = stop.NewPlacer

// Adopt stub (red commit).
func (g *HostGate) Adopt(profile, conv string, pid int, stamp string) (held bool, err error) {
	return false, nil
}

// ReleaseAllHolds stub (red commit).
func (g *HostGate) ReleaseAllHolds() {}

// placer returns the placer of the profile's open run, nil with no run open.
func (g *HostGate) placer(profile, conv string) (stop.Placement, error) {
	if profile != g.Profile {
		return nil, fmt.Errorf("the open-ended mode is wired for profile %q, not %q (conversation %s): nothing was started", g.Profile, profile, conv)
	}
	runs, err := g.Store.OpenRuns(context.Background(), g.Profile)
	if g.activity().failOpen(runs, err, g.RunDir, g.Store, g.Profile) {
		return nil, nil // no run is known and the table cannot be listed: nothing to place
	}
	if err != nil {
		return nil, fmt.Errorf("the run state cannot be read (conversation %s): %w", conv, err)
	}
	if len(runs) == 0 {
		return nil, nil
	}
	pl, err := newPlacer(g.Kernel, g.RunDir, uint64(runs[0].Generation))
	if err != nil {
		return nil, fmt.Errorf("conversation %s: %w", conv, err)
	}
	return pl, nil
}
