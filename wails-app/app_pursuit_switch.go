package main

import (
	"encoding/json"
	"math"
)

// ─── The pursuit switch, its notice and the mission's authority toggles ──────
//
// The logic lives in the CLI; these bindings only run its verbs and return its documents (the app renders, it decides nothing):
// `coder host pursuit status` (the first-run notice, the pause verb, the saved switch, what a restart would change),
// `task session pursuit readiness|pause ID`, `coder sessions mission-children list|enable|disable` and
// `coder sessions settings set --patch` for agent_approval. Every write is the operator's: the CLI refuses it for any process
// inside a coder session or mission task, and the page only reaches it from a click the operator makes on a button.

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

// SetMissionChildren is `coder sessions mission-children enable|disable` (the operator's switch for child sessions).
func (a *App) SetMissionChildren(on bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	verb := "disable"
	if on {
		verb = "enable"
	}
	return a.jsonResult("coder", "sessions", "mission-children", verb)
}

// SetAgentApproval turns the lead's approval of child sessions on (with a spend ceiling above 0, as the CLI requires) or off
// through `coder sessions settings set --patch`. The patch is built here from two typed values, never from page text.
func (a *App) SetAgentApproval(conversationID string, on bool, ceilingUSD float64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	patch := map[string]any{"agent_approval": "off"}
	if on {
		if math.IsNaN(ceilingUSD) || ceilingUSD <= 0 || ceilingUSD > 100000 {
			return taskRefusal("the lead's approval needs a spend ceiling above 0 (and at most 100000)")
		}
		patch = map[string]any{"agent_approval": "lead", "approval_ceiling_usd": ceilingUSD}
	}
	raw, _ := json.Marshal(patch)
	return a.jsonResult("coder", "sessions", "settings", "set", "--patch="+string(raw), "--", conversationID)
}
