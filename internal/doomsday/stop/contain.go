package stop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
)

// ErrContainmentUnavailable means the mode must refuse to start: where no containment mechanism is available the
// mode does not start. The first design forbade any fallback to a process group on the ground that a silent
// weakening is exactly what the contract forbids (note 13.2 section 10). That is REVERSED by the owner's decision of
// 2026-11-06: the open-ended mode is enabled on every platform, and where the mechanism cannot prove that no process
// left the group (a Windows Job Object) it is still used but is HONEST about it: Stop ends it as UnitBestEffort and
// the report says "stopped (best effort: ...)", never a bare "stopped". The weakening is no longer silent, which is
// what the old rule protected. A platform with no mechanism at all still refuses.
var ErrContainmentUnavailable = errors.New("stop: process containment is unavailable; the open-ended mode refuses to start")

// ProbeStep is one named requirement of the probe.
type ProbeStep struct {
	Name  string
	Check func() error
}

// RunProbe runs the steps in order and refuses, naming the first failing one.
func RunProbe(steps []ProbeStep) error {
	for _, s := range steps {
		if err := s.Check(); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrContainmentUnavailable, s.Name, err)
		}
	}
	return nil
}

// KindCgroup2 is the UnitRecord.Kind of the Linux unit: a delegated systemd transient service whose cgroup v2 is
// the containment.
const KindCgroup2 = "cgroup2"

// KindJobObject is the UnitRecord.Kind of the Windows unit: a named Job Object with KILL_ON_JOB_CLOSE, whose
// handle is held by an anchor process so that it outlives the CLI. Best effort (see UnitBestEffort).
const KindJobObject = "jobobject"

// Containment is the per-OS mechanism behind a run's unit. The unit's lifecycle after the proof (enumerate,
// signal, kill, "gone") goes through the Group a Proof carries; Create and Place are the other two moments of the
// lifecycle, so a mechanism is wholly behind this interface. A platform without an entry in platformKinds refuses
// to start.
type Containment interface {
	// Kind is the UnitRecord.Kind this mechanism records and proves.
	Kind() string
	// Probe says whether the mode may start here: nil, or an error satisfying ErrContainmentUnavailable.
	Probe() error
	// Prove is the identity proof of a recorded unit of this kind; any failed check returns ErrNotProven.
	Prove(rec UnitRecord) (Proof, error)
	// Create makes the run's unit at entry, records it durably (the journal and the key-free unit file) and returns
	// the record; any failure is ErrUnitNotCreated and leaves no unit behind ("no unit, no run").
	Create(ctx context.Context, a CreateArgs, gen uint64) (UnitRecord, error)
	// Place returns the placement of a recorded unit: commands that start INSIDE it. It proves the unit on every
	// placement, so a unit that cannot be proven refuses the command.
	Place(rec UnitRecord) (Placement, error)
	// Present says whether the unit still exists after its service/anchor was stopped: it must be absent for the
	// end to be reported. An error means it could not be checked.
	Present(rec UnitRecord) (bool, error)
}

// CreateArgs is what Create needs besides the mechanism's own seam.
type CreateArgs struct {
	Dir     RunDir
	Host    UnitHost // the Linux service manager; a mechanism that has none ignores it
	Rand    io.Reader
	Journal UnitJournal
}

// Placement starts commands inside a run's unit (Placer on Linux).
type Placement interface {
	Place(cmd *exec.Cmd) (release func(), err error)
	PlaceTicket(ticket uint64, cmd *exec.Cmd) (release func(), err error)
	PlaceLeased(cmd *exec.Cmd) error
}

// containments builds the mechanism of a unit kind; k is the kernel seam of the identity proof (nil for a probe)
// and steps are the probe steps. Tests replace an entry to observe that the seam is the only way in.
var containments = map[string]func(k Kernel, steps func() []ProbeStep) Containment{
	KindCgroup2:   func(k Kernel, steps func() []ProbeStep) Containment { return cgroup2Containment{k: k, steps: steps} },
	KindJobObject: func(k Kernel, _ func() []ProbeStep) Containment { return newJobContainment(k) },
}

// platformKinds names the unit kind each GOOS may start; a GOOS absent here is refused. The steps of a probe are the
// Linux steps; a mechanism with its own steps (the Job Object) ignores them.
var platformKinds = map[string]string{"linux": KindCgroup2, "windows": KindJobObject}

// cgroup2Containment is the Linux mechanism; its checks are the existing probe steps and ProveUnit's P1-P6.
type cgroup2Containment struct {
	k     Kernel
	steps func() []ProbeStep
}

func (cgroup2Containment) Kind() string                          { return KindCgroup2 }
func (c cgroup2Containment) Probe() error                        { return RunProbe(c.steps()) }
func (c cgroup2Containment) Prove(rec UnitRecord) (Proof, error) { return proveCgroup2(c.k, rec) }

// Create wraps UnitCreator unchanged.
func (c cgroup2Containment) Create(ctx context.Context, a CreateArgs, gen uint64) (UnitRecord, error) {
	return UnitCreator{K: c.k, Dir: a.Dir, Host: a.Host, Rand: a.Rand, Journal: a.Journal}.Create(ctx, gen)
}

// Place wraps Placer unchanged.
func (c cgroup2Containment) Place(rec UnitRecord) (Placement, error) {
	return &Placer{K: c.k, Rec: rec}, nil
}

// Present opens the unit's cgroup: present when it opens, absent when the kernel says so.
func (c cgroup2Containment) Present(rec UnitRecord) (bool, error) {
	cg, err := c.k.OpenCgroup(rec.CgroupPath)
	switch {
	case err == nil:
		cg.Close()
		return true, nil
	case !c.k.IsAbsent(err):
		return false, err
	}
	return false, nil
}

// probe is the platform decision, with the platform and the Linux steps injectable for tests.
func probe(goos string, linuxSteps func() []ProbeStep) error {
	kind, ok := platformKinds[goos]
	if !ok {
		return fmt.Errorf("%w: %s is not supported (Linux and Windows only)", ErrContainmentUnavailable, goos)
	}
	return containments[kind](nil, linuxSteps).Probe()
}

// Probe is run at entry (before the charter becomes effective) and again at every host start of a running
// generation. A nil result is the only way the mode may start.
func Probe() error { return probe(runtime.GOOS, linuxProbeSteps) }
