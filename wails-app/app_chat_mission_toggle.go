package main

// ─── Mission toggle (the coder chat header) ─────────────────────────────────
//
// Whether a coder chat runs as a mission lives in the CLI:
// `coder sessions settings set --mission on|off`. The state the header shows
// (on, locked by an open mission session, available at this access level, and
// why not) is the "mission" object of `coder sessions settings get`. These two
// bindings only run those commands; the CLI decides who may change what and
// refuses turning it off while the chat has an open mission session.

// GetChatMissionToggle returns `coder sessions settings get`; the header reads
// its "mission" object ({on, locked, sessionId, available, reason}).
func (a *App) GetChatMissionToggle(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "settings", "get", "--", conversationID)
}

// SetChatMissionToggle runs `coder sessions settings set --mission on|off` and
// returns the settings after the change, or the CLI's refusal.
func (a *App) SetChatMissionToggle(conversationID string, on bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	state := "off"
	if on {
		state = "on"
	}
	return a.jsonResult("coder", "sessions", "settings", "set", "--mission="+state, "--", conversationID)
}
