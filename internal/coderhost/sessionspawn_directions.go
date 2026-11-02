package coderhost

import "github.com/monoes/mono-agent/internal/tasks"

// The cap on a lead's open direction sessions (the operator's mission_directions) is held by tasks.Store.CreateSession,
// in the insert transaction, for every agent that creates a direction (see tasks/session_direction_cap.go). The
// settings validation here uses the same hard cap.
const hardMissionDirections = tasks.HardMissionDirections

// cloneInt copies an optional int, so that two Settings values never share the number behind one pointer.
func cloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// EffectiveMissionDirections is K as the cap reads it: the stored number, or the default when none is set (nil or 0).
func (s Settings) EffectiveMissionDirections() int {
	if s.MissionDirections == nil || *s.MissionDirections == 0 {
		return tasks.DefaultMissionDirections
	}
	return min(*s.MissionDirections, hardMissionDirections)
}

// MissionDirectionsExplicit says whether the operator chose K (an explicit 0 counts).
func (s Settings) MissionDirectionsExplicit() bool { return s.MissionDirections != nil }
