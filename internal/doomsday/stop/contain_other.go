//go:build !linux

package stop

// linuxProbeSteps is never consulted off Linux: macOS and Windows have their own containment kinds (platformKinds), which
// ignore the Linux steps.
func linuxProbeSteps() []ProbeStep {
	return []ProbeStep{{Name: "platform", Check: func() error { return ErrContainmentUnavailable }}}
}

// ReadinessSteps is empty off Linux: the read-only containment requirements are the Linux unit's.
func ReadinessSteps() []ReadinessStep { return nil }
