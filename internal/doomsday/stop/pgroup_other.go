//go:build !darwin

package stop

// realPGroupOS is nil off macOS: the process-group mechanism refuses there.
func realPGroupOS() PGroupOS { return nil }

// pgroupHostSteps are the macOS-only requirements of the rest of the mode; off macOS they never run.
func pgroupHostSteps() []ProbeStep { return nil }
