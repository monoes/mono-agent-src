package main

// ─── The pursuit switch, its notice and the mission's authority toggles ──────
//
// The logic lives in the CLI; these bindings only run its verbs and return its documents (the app renders, it decides nothing):
// `coder host pursuit status` (the first-run notice, the pause verb, the saved switch, what a restart would change),
// `task session pursuit readiness|pause ID`, `coder sessions mission-children list|enable|disable` and
// `coder sessions settings set --patch` for agent_approval (the two writes are in app_mission_authority.go, which asks the person
// in a native dialog first).

// PursuitHostStatus is `coder host pursuit status`: notice, pauseCommand, savedOperatorOff, savedJudge, pendingRestart and the
// running host's loop (enabled, missing, pinSource, reason).
func (a *App) PursuitHostStatus() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "host", "pursuit", "status")
}

// PursuitReadiness is `task session pursuit readiness ID`: what is missing for the mission's pursuit, each item with its fix.
func (a *App) PursuitReadiness(sessionId int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(sessionId)
	if bad != "" {
		return bad
	}
	return a.jsonResult("task", "session", "pursuit", "readiness", sid)
}

// PursuitPause is `task session pursuit pause ID`: nothing new starts for that mission (a judge call or a lead turn already
// running finishes and is paid for).
func (a *App) PursuitPause(sessionId int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(sessionId)
	if bad != "" {
		return bad
	}
	return a.jsonResult("task", "session", "pursuit", "pause", sid)
}

// MissionChildrenState is `coder sessions mission-children list`; its missionChildrenEnabled is the switch.
func (a *App) MissionChildrenState() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "mission-children", "list")
}
