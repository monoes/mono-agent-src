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

// probe is the platform decision, with the platform and the Linux steps injectable for tests.
func probe(goos string, linuxSteps func() []ProbeStep) error {
	if goos != "linux" {
		return fmt.Errorf("%w: %s is not supported (Linux only)", ErrContainmentUnavailable, goos)
	}
	return RunProbe(linuxSteps())
}

// Probe is run at entry (before the charter becomes effective) and again at every host start of a running
// generation. A nil result is the only way the mode may start.
func Probe() error { return probe(runtime.GOOS, linuxProbeSteps) }
