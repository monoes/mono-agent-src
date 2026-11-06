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

// ─── Chat mode (off | mission | open) ───────────────────────────────────────
//
// The mode is chosen before the first message and locked after it; the CLI
// enforces both. GetChatMissionToggle's answer carries it as "chatMode"
// ({mode, locked, reason, options}); a chat that does not exist yet asks for the
// options of its access level; the mode of a new chat goes to
// CreateChatConversationAccess, which sets it with the conversation.

// SetChatMode runs `coder sessions settings set --chat-mode MODE` and returns the
// settings after the change, or the CLI's refusal (locked, not available, access).
func (a *App) SetChatMode(conversationID, mode string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "settings", "set", "--chat-mode="+mode, "--", conversationID)
}

// GetChatModeOptions runs `coder sessions chat-modes --access LEVEL`: the three
// choices and why a disabled one is disabled, for a chat that is not made yet.
func (a *App) GetChatModeOptions(access string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "chat-modes", "--access="+access)
}
