package coderhost

import "github.com/monoes/mono-agent/internal/tasks"

// The cap on a lead's open direction sessions (the operator's mission_directions) is held by tasks.Store.CreateSession,
// in the insert transaction, for every agent that creates a direction (see tasks/session_direction_cap.go). The
// settings validation here uses the same hard cap.
const hardMissionDirections = tasks.HardMissionDirections
