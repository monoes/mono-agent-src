//go:build !linux

package stop

// linuxProbeSteps is never consulted off Linux (probe refuses on the platform first); the mode does not start
// on macOS or Windows.
func linuxProbeSteps() []ProbeStep {
	return []ProbeStep{{Name: "platform", Check: func() error { return ErrContainmentUnavailable }}}
}

// ReadinessSteps is empty off Linux: the read-only containment requirements are the Linux unit's.
func ReadinessSteps() []ReadinessStep { return nil }
