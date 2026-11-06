package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// VersionInfo is returned by GetVersion.
type VersionInfo struct {
	Version   string `json:"version"`
	BuildDate string `json:"build_date"`
}

// UpdateInfo is returned by CheckForUpdate.
type UpdateInfo struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
	Error           string `json:"error,omitempty"`
}

// UpdateResult is returned by AppSelfUpdate.
type UpdateResult struct {
	Success    bool   `json:"success"`
	UpToDate   bool   `json:"up_to_date,omitempty"` // nothing was installed
	NewVersion string `json:"new_version,omitempty"`
	Error      string `json:"error,omitempty"`
	// Notice is what the person should read before restarting the app (set when the update did not restart the daemon or the
	// bridge, or left another CLI old); the app does not quit by itself then.
	Notice string `json:"notice,omitempty"`
}

// GetVersion returns the current build version.
func (a *App) GetVersion() VersionInfo {
	if a.signedOut() != nil {
		return VersionInfo{}
	}
	return VersionInfo{
		Version:   version,
		BuildDate: buildDate,
	}
}

// CheckForUpdate asks the CLI whether a newer release exists for this
// app's own version (`monoagentcli update --check --current <version>`):
// the release lookup lives in the CLI, the app only shows the answer.
func (a *App) CheckForUpdate() UpdateInfo {
	info := UpdateInfo{CurrentVersion: version}
	out := a.rawCLI(updateCheckTimeout, "update", "--check", "--current", version)
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return UpdateInfo{CurrentVersion: version, Error: fmt.Sprintf("update check: %v", err)}
	}
	if info.CurrentVersion == "" {
		info.CurrentVersion = version
	}
	return info
}

// updateCheckTimeout bounds `update --check` (one GitHub request).
const updateCheckTimeout = 30 * time.Second

// backgroundUpdateCheck runs once on startup (after a short delay) and then
// every 24 hours, emitting "update:available" when a newer release exists.
func (a *App) backgroundUpdateCheck() {
	time.Sleep(10 * time.Second)
	if info := a.CheckForUpdate(); info.UpdateAvailable {
		eventsEmit(a.ctx, "update:available", info)
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if info := a.CheckForUpdate(); info.UpdateAvailable {
				eventsEmit(a.ctx, "update:available", info)
			}
		case <-a.ctx.Done():
			return
		}
	}
}
