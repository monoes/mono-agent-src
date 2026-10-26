package main

// ─── Coder session settings (the pane) ──────────────────────────────────────
//
// The settings of a coder session (check-ins, autonomy, limits, mesh) and
// whether it may run unattended live in the CLI: `coder sessions settings`.
// These two bindings only run it; the CLI validates and enforces who may change
// what.

// GetCoderSessionSettings returns `coder sessions settings get`: the settings,
// the unattended flag, the access level, which settings are the operator's and
// the values each choice allows.
func (a *App) GetCoderSessionSettings(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("coder", "sessions", "settings", "get", "--", conversationID)
}

// SetCoderSessionSettings runs `coder sessions settings set`: patch is a JSON
// object naming only the settings to change (or ""), unattended is "true",
// "false" or "". It returns the settings after the change.
func (a *App) SetCoderSessionSettings(conversationID, patch, unattended string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"coder", "sessions", "settings", "set"}
	if patch != "" {
		args = append(args, "--patch="+patch)
	}
	if unattended != "" {
		args = append(args, "--unattended="+unattended)
	}
	return a.jsonResult(append(args, "--", conversationID)...)
}
