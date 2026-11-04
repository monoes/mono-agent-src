package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	recordcli "github.com/monoes/mono-agent/internal/cli/record"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `record` lives in internal/cli/record; this is its adapter: the CLI's globals and helpers
// handed over as recordcli.Deps, and the browser connection replay and verify drive.

// recordVerifyExec returns the replay function for verify, or an error
// when no browser is reachable.
func recordVerifyExec(ctx context.Context, automationID string, verbose, keepOpen bool, secrets func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
	logger := logging.Zerolog().With().Str("component", "extension").Logger()
	if verbose {
		logger = logger.Level(zerolog.TraceLevel)
	}
	// Same connect path as `node run`: reuse or start the bridge, and when
	// the extension is not attached launch the user's real Chrome and wait.
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return nil, perr
	}
	bridge := setupProfileBridge(profile, logger, 3*time.Second)
	if !bridge.IsConnected() {
		if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
			return nil, fmt.Errorf("browser bridge not connected: %w", err)
		}
	}
	provider := &browserpkg.HybridSessionProvider{ExtBridge: bridge, Logger: logger}
	page, err := provider.GetPage(ctx, verifyStartPlatform(ctx, automationID), "")
	if err != nil {
		return nil, fmt.Errorf("browser bridge not connected: %w", err)
	}
	return recordanalyze.PageExecWith(page, logger, recordanalyze.PageExecOptions{Secrets: secrets, KeepOpen: keepOpen}), nil
}

// verifyStartPlatform is whose start page a verify's tab opens on: the
// draft's automation, or none (a blank tab) for a verify that stops at the
// first page step (the extension's), since the package's start URL is a
// file anyone can write and opening it is a request with the user's cookies.
func verifyStartPlatform(ctx context.Context, automationID string) string {
	if recordanalyze.StopsAtActions(ctx) {
		return ""
	}
	return automationID
}

// recordReplayPage opens the tab a replay drives — the same connect path
// as verify.
func recordReplayPage(ctx context.Context, verbose bool) (browserpkg.PageInterface, zerolog.Logger, error) {
	logger := logging.Zerolog().With().Str("component", "replay").Logger()
	if verbose {
		logger = logger.Level(zerolog.TraceLevel)
	}
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return nil, logger, perr
	}
	bridge := setupProfileBridge(profile, logger, 3*time.Second)
	if !bridge.IsConnected() {
		if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
			return nil, logger, fmt.Errorf("browser bridge not connected: %w", err)
		}
	}
	provider := &browserpkg.HybridSessionProvider{ExtBridge: bridge, Logger: logger}
	page, err := provider.GetPage(ctx, recordanalyze.ReplayAutomationID, "")
	if err != nil {
		return nil, logger, fmt.Errorf("browser bridge not connected: %w", err)
	}
	return page, logger, nil
}

func recordDeps(cfg *globalConfig) recordcli.Deps {
	d := recordcli.Deps{
		JSONOutput:     func() bool { return cfg.JSONOutput },
		Verbose:        func() bool { return cfg.Verbose },
		ProfileID:      func() string { return cfg.ProfileID },
		BrowserProfile: func() string { return browserProfile(cfg) },
		ResolveProfile: func(id string) string {
			if db, err := openProfileDB(cfg.DBPath); err == nil {
				p, rerr := resolveCaptureProfile(db.DB, id)
				db.Close()
				if rerr == nil {
					return p.ID
				}
			}
			return id
		},
		OpenDB: func() (*storage.Database, error) { return initDB(cfg) },
		StoreWorkflow: func(ctx context.Context, db *storage.Database, wf *workflow.Workflow) error {
			return createOrOverwriteWorkflowAtomically(ctx, openWorkflowStore(db), wf, false)
		},
		OpenRegistry:    openAutomationRegistry,
		InvalidInput:    errInvalidInput,
		Reported:        func(err error) error { return reportedError{Err: err} },
		WriteJSON:       writeJSONTo,
		WithJSONErrors:  func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) },
		StdinIsTerminal: stdinIsTerminal,
		Refusal:         opgate.Refusal,
		AnalyzeRunner:   recordcli.ExecRunner,
		VerifyExec:      recordVerifyExec,
		ReplayPage:      recordReplayPage,
	}
	d.SecretLookup = d.VaultSecretLookup
	d.SelfHeal = d.JevSelfHeal
	return d
}

// newRecordCmd builds `monoagentcli record …` (extension activity recordings).
func newRecordCmd(cfg *globalConfig) *cobra.Command {
	return recordcli.NewCommand(recordDeps(cfg))
}
