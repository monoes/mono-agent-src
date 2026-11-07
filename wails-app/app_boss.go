package main

// ─── Steering of an open-ended run's boss ───────────────────────────────────
//
// In a chat whose open-ended run is going the person's text is steering for the boss behind the chat, never an
// answer: `coder sessions steer CHAT TEXT` wraps it as untrusted guidance and queues it for the boss's conversation.
// This binding only runs that command; the CLI decides who may steer and refuses when no boss stands behind the chat.

// SteerBoss runs `coder sessions steer` and returns its JSON ({commandId, conversationId, turnId}), or the CLI's refusal.
func (a *App) SteerBoss(conversationID, text string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "steer", "--", conversationID, text)
}
