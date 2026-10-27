package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/cli/enginebuild"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// openWorkflowStore returns the workflow store shared by the CLI and the
// desktop app: JSON files under ~/.monoagent/workflows plus the SQLite
// database, so a workflow made by either side shows up on the other.
func openWorkflowStore(db *storage.Database) *workflow.HybridWorkflowStore {
	sqlStore := workflow.NewSQLiteWorkflowStore(db.DB)
	dir := expandPath("~/.monoagent/workflows")
	fileStore, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		// No usable workflow directory: keep the same return type, backed by
		// SQLite alone.
		return withGrantTierRefresh(db, workflow.NewHybridWorkflowStore(nil, sqlStore))
	}
	// Workflows saved file-only (the desktop editor, older CLI versions)
	// get their SQLite rows backfilled; cheap after the first run.
	synced, _ := syncFileWorkflowsToSQL(context.Background(), db.DB, dir)
	store := withGrantTierRefresh(db, workflow.NewHybridWorkflowStore(fileStore, sqlStore))
	for _, id := range synced {
		raiseGrantTiersForWorkflow(context.Background(), db, store.GetWorkflow, id)
	}
	return store
}

// withGrantTierRefresh makes every save through store re-derive the tiers
// of the saved workflow's org grants (#284).
func withGrantTierRefresh(db *storage.Database, store *workflow.HybridWorkflowStore) *workflow.HybridWorkflowStore {
	store.SetOnSaved(func(ctx context.Context, id string) {
		raiseGrantTiersForWorkflow(ctx, db, store.GetWorkflow, id)
	})
	return store
}

// createOrOverwriteWorkflowAtomically persists wf (metadata + nodes +
// connections) via store as ONE publication: the file and the rows change
// together or not at all, so a failure leaves the store exactly as it was —
// including the previous version of an existing workflow, byte for byte.
// Shared by `workflow import`, `workflow templates use`, duplicate and record,
// the commands that create a workflow's full node/connection set in one shot.
//
// Callers must have already: finalized every id on wf/wf.Nodes/
// wf.Connections (this function does not mint ids), and validated
// wf.Connections against wf.Nodes via workflow.ValidateConnectionsAgainstNodes:
// the common failure (a dangling connection reference) should be caught in
// memory, before the store is ever invoked.
func createOrOverwriteWorkflowAtomically(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, _ bool) error {
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		return fmt.Errorf("save workflow: %w", err)
	}
	return nil
}

// buildEngine constructs a fully wired WorkflowEngine suitable for CLI use
// (enginebuild.Build, with the CLI's database, browser session provider,
// bot registry and workflow store).
func buildEngine(cfg *globalConfig, allowAllProfiles bool) (*workflow.WorkflowEngine, func(), error) {
	return enginebuild.Build(enginebuild.Options{
		Verbose:          cfg.Verbose,
		ProfileID:        func() string { return cfg.ProfileID },
		AllowAllProfiles: allowAllProfiles,
	}, enginebuild.Deps{
		OpenDB: func() (*storage.Database, error) { return initDB(cfg) },
		NewSessionProvider: func(logger zerolog.Logger, defaultProfile string) enginebuild.SessionProvider {
			return &lazyBrowserSessionProvider{logger: logger, defaultProfile: defaultProfile}
		},
		BotRegistry:       &cliBotRegistry{},
		OpenWorkflowStore: openWorkflowStore,
	})
}

// lazyBrowserSessionProvider implements nodes.SessionProvider by connecting
// the Chrome extension bridge on first use instead of at engine build time.
// Browser nodes call GetPage when they execute, so the connect sequence
// runs only for workflows that actually contain a browser node.
//
// Each GetPage is narrowed to the browser bound to the run's profile: the
// execution's profile id rides ctx (WorkflowEngine.runExecution), so a
// daemon running many profiles' workflows sends each one to its own
// browser. Only building the shared bridge is serialised; waiting for a
// profile's browser is not, so one profile's slow browser never holds up
// another's run.
type lazyBrowserSessionProvider struct {
	logger         zerolog.Logger
	defaultProfile string // the engine's profile, for a ctx that carries none

	// setup and ensure default to setupExtensionBridge (3s wait) and
	// ensureExtensionConnected; tests replace them.
	setup  func() browserpkg.ExtensionBridge
	ensure func(connChecker, time.Duration) error

	mu     sync.Mutex
	bridge browserpkg.ExtensionBridge
}

func (p *lazyBrowserSessionProvider) base() browserpkg.ExtensionBridge {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bridge == nil {
		if p.setup != nil {
			p.bridge = p.setup()
		} else {
			p.bridge = setupExtensionBridge(p.logger, 3*time.Second)
		}
	}
	return p.bridge
}

func (p *lazyBrowserSessionProvider) GetPage(ctx context.Context, platform, username string) (browserpkg.PageInterface, error) {
	profileID, ok := vault.ProfileIDInContext(ctx)
	if !ok {
		profileID = p.defaultProfile
	}
	bridge := bridgeForProfile(p.base(), profileID)
	ensure := p.ensure
	if ensure == nil {
		ensure = ensureExtensionConnected
	}
	// No throwaway automation browser. When this profile's browser is not
	// attached, launch the user's real Chrome (same mechanism as `login`)
	// and wait for it.
	if err := ensure(bridge, 30*time.Second); err != nil {
		return nil, fmt.Errorf("browser session: %w", err)
	}
	return (&browserpkg.HybridSessionProvider{ExtBridge: bridge, Logger: p.logger}).GetPage(ctx, platform, username)
}

// Close matches the cleanup signature buildEngine hands back. There is
// nothing to tear down: HybridSessionProvider.Close is a no-op, and the
// extension server (when the lazy connect ever ran) is a process-wide
// singleton on a fixed port that outlives individual engine runs.
func (p *lazyBrowserSessionProvider) Close() {}

// newWorkflowCmd returns the parent `workflow` cobra command with all subcommands attached.
func newWorkflowCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: i18n.T("workflow.short"),
		Long:  i18n.T("workflow.long"),
	}

	cmd.AddCommand(
		newWorkflowListCmd(cfg),
		newWorkflowGetCmd(cfg),
		newWorkflowCreateCmd(cfg),
		newWorkflowImportCmd(cfg),
		newWorkflowConvertCmd(cfg),
		newWorkflowExportCmd(cfg),
		newWorkflowRunCmd(cfg),
		newWorkflowInputsCmd(cfg),
		newWorkflowValidateCmd(cfg),
		newWorkflowCheckKeysCmd(cfg),
		newWorkflowActivateCmd(cfg),
		newWorkflowDeactivateCmd(cfg),
		newWorkflowDeleteCmd(cfg),
		newWorkflowExecutionsCmd(cfg),
		newWorkflowExecutionCmd(cfg),
		newWorkflowCancelCmd(cfg),
		newWorkflowRetryCmd(cfg),
		newWorkflowEvaluateCmd(cfg),
		newWorkflowEvaluationsCmd(cfg),
		newWorkflowSaveCmd(cfg),
		newWorkflowNodeCmd(cfg),
		newWorkflowConnectCmd(cfg),
		newWorkflowDisconnectCmd(cfg),
		newWorkflowMigrateCmd(cfg),
		newWorkflowTemplatesCmd(cfg),
		newWorkflowSearchCmd(cfg),
		newWorkflowVersionsCmd(cfg),
		newWorkflowGenerateCmd(cfg),
	)
	cmd.AddCommand(newWorkflowAitaskCmds(cfg)...)

	return cmd
}
