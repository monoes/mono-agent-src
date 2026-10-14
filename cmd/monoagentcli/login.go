package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	logincli "github.com/monoes/mono-agent/internal/cli/login"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/storage"
)

// `login` and `logout` live in internal/cli/login; this is their adapter: the CLI's globals
// and helpers handed over as logincli.Deps, and the browser connection the login tabs use.

// newExtensionBridgeLogger builds the same warn-level stderr logger used by
// `node run`/`workflow run` when talking to the Chrome extension bridge.
func newExtensionBridgeLogger() zerolog.Logger {
	return logging.Zerolog().With().Str("component", "extension").Logger()
}

// openLoginRegistry returns the booted automation registry, or nil when there
// is none. Test binaries get nil unless a test swaps this out, so they never
// touch the real ~/.monoagent.
var openLoginRegistry = func() (*automation.Registry, error) {
	if testing.Testing() {
		return nil, nil
	}
	return nodes.BootAutomations("")
}

// connectLoginBridge is the bridge to profileID's browser once the extension is connected.
func connectLoginBridge(profileID string) (browserpkg.ExtensionBridge, error) {
	bridge := setupProfileBridge(profileID, newExtensionBridgeLogger(), 3*time.Second)
	if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
		return nil, err
	}
	return bridge, nil
}

func loginDeps(cfg *globalConfig) logincli.Deps {
	return logincli.Deps{
		JSONOutput:     func() bool { return cfg.JSONOutput },
		ProfileID:      func() string { return cfg.ProfileID },
		BrowserProfile: func() string { return browserProfile(cfg) },
		OpenDB:         func() (*storage.Database, error) { return initDB(cfg) },
		OpenRegistry:   func() (*automation.Registry, error) { return openLoginRegistry() },
		ConnectBridge:  connectLoginBridge,
		NotFound:       errNotFound,
		InvalidInput:   errInvalidInput,
		AuthConnection: errAuthConnection,
		WriteJSON:      writeJSONTo,
		NewTable:       newPlainTable,
	}
}

func newLoginCmd(cfg *globalConfig) *cobra.Command { return logincli.NewCommand(loginDeps(cfg)) }

func newLogoutCmd(cfg *globalConfig) *cobra.Command {
	return logincli.NewLogoutCommand(loginDeps(cfg))
}

// profileOrDefault is the profile a command acts on: --profile (or the
// active one initDB resolved), "default" when neither is set.
func profileOrDefault(cfg *globalConfig) string {
	if cfg.ProfileID == "" {
		return "default"
	}
	return cfg.ProfileID
}

// upsertSessionRow stores platform/username's cookie jar as a "session"-kind
// vault entry and points the crawler_sessions row at it (internal/cli/login).
func upsertSessionRow(ctx context.Context, db *sql.DB, profileID, platform, username string, cookiesJSON []byte) error {
	return logincli.UpsertSessionRow(ctx, db, profileID, platform, username, cookiesJSON)
}
