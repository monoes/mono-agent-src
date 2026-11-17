package chat

import "time"

// slowLaunchStep is how long one step of a turn's launch may take before the host log names it.
const slowLaunchStep = 2 * time.Second

// launchStepTimer returns a mark(step) that logs only when the step just finished took at least slow, so a launch
// that stalls says which step it stalled in while a healthy one stays silent. It logs the turn id and step names only.
func launchStepTimer(logf func(string, ...any), turnID string, slow time.Duration, now func() time.Time) func(step string) {
	start := now()
	last := start
	return func(step string) {
		t := now()
		d := t.Sub(last)
		last = t
		if logf != nil && d >= slow {
			logf("turn %s launch: slow step %q took %v (%v since the launch began)", turnID, step, d.Round(time.Millisecond), t.Sub(start).Round(time.Millisecond))
		}
	}
}
