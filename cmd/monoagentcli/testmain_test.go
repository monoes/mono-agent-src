package main

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/release"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent. The app no longer ships the
// official packages; tests get them from the repo's automations/ source,
// seeded into each test home and readable without a registry.
//
// The OS keychain is mocked for the whole binary: initDB migrates vault
// sessions, which reads the keychain, and a test that did not mock it itself
// hung on a keychain prompt (or an unanswered secret service) whenever it was
// selected alone with -run (#363).
func TestMain(m *testing.M) {
	keyring.MockInit()
	automation.TestSeed = automations.FS()
	action.SetTestFallback(automations.FS())
	// The live signed manifest is published now; tests that exercise the
	// updaters must not reach it.
	releaseClient = func() *release.Client { return &release.Client{} }
	// The legacy GitHub update path is off in shipped builds (release.AllowLegacyGitHubUpdates); the
	// updater tests that drive it opt in, as in internal/appupdate and internal/cli/update.
	release.AllowLegacyGitHubUpdates = true
	testhome.Main(m)
}
