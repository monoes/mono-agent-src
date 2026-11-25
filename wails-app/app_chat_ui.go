package main

// ─── A2UI surfaces in chat ──────────────────────────────────────────────────
//
// What a pressed surface button does, and which pending approvals have a card,
// is decided by the CLI (`chat ui action`, `chat ui sync-hil`). These bindings
// only run those commands under the app's explicit profile and relay their
// JSON. The envelope goes as one exec argument, never through a shell string.

// ChatUIAction runs `chat ui action --json ENVELOPE -- CONVERSATION` and returns
// its answer: {outcome, reason?, delivered?, message?, ...}. A press the CLI
// refuses comes back as an outcome of rejected or stale, not as an error.
func (a *App) ChatUIAction(conversationID, actionJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("chat", "ui", "action", "--json", actionJSON, "--", conversationID)
}

// ChatUISyncHIL runs `chat ui sync-hil -- CONVERSATION` and returns
// {"surfaced":[ids],"resolved":[ids]}. The cards themselves arrive through the
// normal chat:event stream.
func (a *App) ChatUISyncHIL(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("chat", "ui", "sync-hil", "--", conversationID)
}
