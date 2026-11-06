//go:build !windows

package stop

// realJobOS is nil off Windows: the Job Object mechanism refuses there.
func realJobOS() JobOS { return nil }

// windowsHostSteps are the Windows-only requirements of the rest of the mode; off Windows they never run.
func windowsHostSteps() []ProbeStep { return nil }
