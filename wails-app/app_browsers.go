package main

// Settings → Browsers: which browser each profile's automations run in. Any
// number of profiles may share a browser. Everything shells out to
// `monoagentcli extension browsers|assign|bind|unbind`; the binding itself is
// stored in each browser's extension.

import (
	"errors"
	"strings"
)

// BrowserRow mirrors one row of `extension browsers --json`.
type BrowserRow struct {
	Instance string `json:"instance"`
	Label    string `json:"label"`
	// ProfileID/ProfileName are the first profile, for older CLIs.
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	// ProfileIDs/ProfileNames are every profile this browser runs.
	ProfileIDs   []string `json:"profile_ids"`
	ProfileNames []string `json:"profile_names"`
	Legacy       bool     `json:"legacy"`
	Conflict     bool     `json:"conflict"`
	Version      string   `json:"version"`
	ConnectedAt  string   `json:"connected_at"`
}

// AssignReport mirrors `extension assign --json`.
type AssignReport struct {
	ProfileID string       `json:"profile_id"`
	Instance  string       `json:"instance"`
	Changed   []BrowserRow `json:"changed"`
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

// AssignBrowser makes instance the only browser profileID runs in; an empty
// instance removes the profile from every browser (the default browser
// runs it again). Other profiles in that browser are left alone. The logic
// lives in `extension assign`.
func (a *App) AssignBrowser(profileID, instance string) (*AssignReport, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, errors.New("no profile given")
	}
	args := []string{"extension", "assign", profileID}
	if i := strings.TrimSpace(instance); i != "" {
		args = append(args, i)
	}
	var r AssignReport
	if err := a.runMonoCLI("", &r, args...); err != nil {
		return nil, err
	}
	if r.Changed == nil {
		r.Changed = []BrowserRow{}
	}
	return &r, nil
}

// BindBrowser adds a profile to a browser's profiles; an empty profileID
// makes the browser run none (a default browser again).
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
