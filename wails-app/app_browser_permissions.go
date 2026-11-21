package main

// ─── Agent browser permissions (plan C8) ────────────────────────────────────
//
// What AI agents may do in the user's browsers: a level per agent kind
// (coder, org, chat, other) and per-site allow/block rules. They are
// machine-wide settings `monoagentcli browser permissions` owns; these
// bindings build its argv and return its JSON, or its {"error","code"}. The
// positional arguments go after "--", so a typed origin can never read as a
// flag.

// BrowserPermissions is `browser permissions --json`: {levels, sites,
// kinds:[{kind,label,level,isDefault}], catalog:[{id,label,description}], you}.
func (a *App) BrowserPermissions() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("browser", "permissions")
}

// SetBrowserPermission sets one agent kind's level: `browser permissions set
// <kind> <level>`.
func (a *App) SetBrowserPermission(kind, level string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("browser", "permissions", "set", "--", kind, level)
}

// ApplyBrowserPermissionPreset sets every agent kind's level at once, in one
// save, with site rules left as they are: `browser permissions preset
// <ask|free|off>`. Same answer as SetBrowserPermission.
func (a *App) ApplyBrowserPermissionPreset(preset string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("browser", "permissions", "preset", "--", preset)
}

// SetBrowserSiteRule allows or blocks a site for every agent kind, or clears
// its rule: `browser permissions site <origin> <allow|block|clear>`.
func (a *App) SetBrowserSiteRule(origin, rule string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("browser", "permissions", "site", "--", origin, rule)
}
