package doomsday

import "context"

// FirstTurnState is where the boss's first turn stands: "started" (a turn exists), "pending" (queued, not yet taken), "failed"
// (the host refused or could not run it: Error says why) or "none" (nothing was ever queued for the boss).
type FirstTurnState struct {
	State string `json:"first_turn,omitempty"`
	Error string `json:"first_turn_error,omitempty"`
}

// FirstTurnOf reads the first turn of the profile's open run's boss. A profile with no open run, or a run without a recorded
// boss, answers the zero state.
func (w Wiring) FirstTurnOf(context.Context) (FirstTurnState, error) { return FirstTurnState{}, nil }
