package main

import (
	"os/exec"

	"github.com/monoes/mono-agent/internal/chromium"
)

// ─── Chromium browsers (onboarding step "Connect your browser") ─────────────
//
// The onboarding lists the Chromium-based browsers on this machine and can
// open one, so the person can load the extension in it. The frontend names a
// browser by id only: the program that runs comes from the path detected
// here, never from the frontend.

// DetectChromiumBrowsers lists the seven known browsers, installed first;
// nothing while signed out (the account gate, like every binding).
func (a *App) DetectChromiumBrowsers() []chromium.Browser {
	if err := a.signedOut(); err != nil {
		return nil
	}
	return chromium.Detect(chromium.Exists)
}

// OpenChromiumBrowser starts (or brings to the front) the installed browser
// id. An unknown or missing browser is an error.
func (a *App) OpenChromiumBrowser(id string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	name, args, err := chromium.LaunchCommand(id)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap the child (macOS `open` exits at once)
	return nil
}
