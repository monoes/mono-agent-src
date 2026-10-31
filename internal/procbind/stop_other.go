//go:build !linux

package procbind

import "time"

type StopOutcome int

const (
	StopAbsent StopOutcome = iota
	StopStopped
	StopHeld
)

type StopResult struct {
	Outcome StopOutcome
	Cause   string
}

// Stopper never stops anything off Linux: no bound turn exists there, and an unknown one is held.
type Stopper struct {
	StampOf func(pid int) string
	Grace   time.Duration
}

func (s Stopper) StopLeader(int, string) StopResult {
	return StopResult{Outcome: StopHeld, Cause: "the binding service needs Linux"}
}
