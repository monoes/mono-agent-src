package main

// Settings → Browsers: which browser each profile's automations run in.
// Everything shells out to `monoagentcli extension browsers|bind|unbind`;
// the binding itself is stored in each browser's extension.

import (
	"errors"
	"strings"
)

// BrowserRow mirrors one row of `extension browsers --json`.
type BrowserRow struct {
	Instance    string `json:"instance"`
	Label       string `json:"label"`
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	Legacy      bool   `json:"legacy"`
	Conflict    bool   `json:"conflict"`
	Version     string `json:"version"`
	ConnectedAt string `json:"connected_at"`
}

// BrowsersReport mirrors `extension browsers --json`.
type BrowsersReport struct {
	Running  bool         `json:"running"`
	Hint     string       `json:"hint"`
	Browsers []BrowserRow `json:"browsers"`
}

// GetBrowsers lists the browsers attached to the bridge.
func (a *App) GetBrowsers() (*BrowsersReport, error) {
	var r BrowsersReport
	if err := a.cliJSON(profileCLITimeout, &r, "extension", "browsers"); err != nil {
		return nil, err
	}
	if r.Browsers == nil {
		r.Browsers = []BrowserRow{}
	}
	return &r, nil
}

// BindBrowser binds a browser to a profile; an empty profileID unbinds it.
func (a *App) BindBrowser(instance, profileID string) (*BrowserRow, error) {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return nil, errors.New("no browser given")
	}
	args := []string{"extension", "unbind", instance}
	if p := strings.TrimSpace(profileID); p != "" {
		args = []string{"extension", "bind", instance, p}
	}
	var row BrowserRow
	if err := a.runMonoCLI("", &row, args...); err != nil {
		return nil, err
	}
	return &row, nil
}

// GetExtensionStatus returns `monoagentcli extension status --json`
// verbatim: whether a bridge is running, whether the extension is attached,
// and whether this client's pairing token is accepted (clientPairing). The
// dashboard reads it so "connected" means usable, not just "port open".
func (a *App) GetExtensionStatus() string {
	return a.rawCLI(profileCLITimeout, "extension", "status")
}

// PairExtension returns `monoagentcli extension pair --open --json`
// verbatim: it opens the running bridge's one-time pairing page and says
// whether it did, or what to do instead ({opened, url, fallback, hint,
// error}; never the token itself).
func (a *App) PairExtension() string {
	return a.rawCLI(profileCLITimeout, "extension", "pair", "--open")
}
