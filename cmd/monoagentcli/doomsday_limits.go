package main

import "github.com/monoes/mono-agent/internal/doomsday/finish"

// runCriterionLimitsHelper is the limits-helper mode of the finishing's criteria (see finish.RunLimitsHelperIfRequested
// for why it is safe to reach with any arguments). It lives in a doomsday* file because the agent-reachable-surface
// guard allows only those files to name the mode's packages; main.go calls it, by a name that does not.
func runCriterionLimitsHelper(args []string) { finish.RunLimitsHelperIfRequested(args) }
