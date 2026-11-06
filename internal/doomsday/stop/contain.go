package stop

import (
	"errors"
	"fmt"
	"runtime"
)

// ErrContainmentUnavailable means the mode must refuse to start: where the containment primitive is not
// available there is no fallback to a process group, because a fallback that silently weakens the guarantee is
// exactly what the contract forbids (note 13.2 section 10).
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

// Containment is the per-OS mechanism behind a run's unit. The unit's lifecycle after the proof (enumerate,
// signal, kill, "gone") goes through the Cgroup a Proof carries; a mechanism that is not a cgroup generalises that
// type when it arrives. A platform without an entry in platformKinds refuses to start: there is no fallback.
type Containment interface {
	// Kind is the UnitRecord.Kind this mechanism records and proves.
	Kind() string
	// Probe says whether the mode may start here: nil, or an error satisfying ErrContainmentUnavailable.
	Probe() error
	// Prove is the identity proof of a recorded unit of this kind; any failed check returns ErrNotProven.
	Prove(rec UnitRecord) (Proof, error)
}

// containments builds the mechanism of a unit kind; k is the kernel seam of the identity proof (nil for a probe)
// and steps are the probe steps. Tests replace an entry to observe that the seam is the only way in.
var containments = map[string]func(k Kernel, steps func() []ProbeStep) Containment{
	KindCgroup2: func(k Kernel, steps func() []ProbeStep) Containment { return cgroup2Containment{k: k, steps: steps} },
}

// platformKinds names the unit kind each GOOS may start; a GOOS absent here is refused.
var platformKinds = map[string]string{"linux": KindCgroup2}

// cgroup2Containment is the Linux mechanism; its checks are the existing probe steps and ProveUnit's P1-P6.
type cgroup2Containment struct {
	k     Kernel
	steps func() []ProbeStep
}

func (cgroup2Containment) Kind() string                          { return KindCgroup2 }
func (c cgroup2Containment) Probe() error                        { return RunProbe(c.steps()) }
func (c cgroup2Containment) Prove(rec UnitRecord) (Proof, error) { return proveCgroup2(c.k, rec) }

// probe is the platform decision, with the platform and the Linux steps injectable for tests.
func probe(goos string, linuxSteps func() []ProbeStep) error {
	kind, ok := platformKinds[goos]
	if !ok {
		return fmt.Errorf("%w: %s is not supported (Linux only)", ErrContainmentUnavailable, goos)
	}
	return containments[kind](nil, linuxSteps).Probe()
}

// Probe is run at entry (before the charter becomes effective) and again at every host start of a running
// generation. A nil result is the only way the mode may start.
func Probe() error { return probe(runtime.GOOS, linuxProbeSteps) }
