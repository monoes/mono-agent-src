package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/logging"

	aichat "github.com/monoes/mono-agent/internal/ai/chat"
	"github.com/monoes/mono-agent/internal/cli/workflowio"
	cfgpkg "github.com/monoes/mono-agent/internal/config"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/openaiapi"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/scheduler"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runtime holds the lazily-bootstrapped database, store, and (on first
// workflow_run of a profile) a running WorkflowEngine for that profile. It
// mirrors the builder logic in cmd/monoagentcli (initDB + buildEngine)
// without importing cmd. It holds no profile: every call names its own (see
// profile_arg.go), and what is per profile (an engine, the chat tools) is
// keyed by it.
type runtime struct {
	db       *storage.Database
	store    *workflow.HybridWorkflowStore
	registry *workflow.NodeTypeRegistry

	// engines and scheds are one engine and its scheduler per profile: an
	// engine refuses a workflow of any other profile than its own, which is
	// what keeps a run of profile A from touching profile B.
	engines map[string]*workflow.WorkflowEngine
	scheds  map[string]*scheduler.Scheduler
	// runCtx is the long-lived context the engines run under, made on the
	// first engine start (under mu) and cancelled by Close.
	runCtx  context.Context
	runStop context.CancelFunc
	chat    map[string]*aichat.MonoagentTools
	// untrusted names the untrusted content (synced messages) any profile's
	// chat tools returned. Their injection gate is per session, and a server
	// is one session: once content nobody vouches for entered it, every
	// profile's tools are marked.
	untrusted string

	// catalog is the one catalog of models behind api_models_list, made on first
	// use (see modelCatalog). It has its own Once and not mu, which ensureEngine
	// holds across engine.Start.
	catalogOnce sync.Once
	catalog     *openaiapi.Catalog

	// mu guards the lazy engine/registry/scheduler/chat bootstrap: requests
	// are dispatched concurrently, so several tools/call invocations may
	// race to start any of these on first use.
	mu sync.Mutex
}

// chatTools lazily builds the MonoagentTools instance of one profile backing
// every chat-tool-adapted MCP tool (monoagent_tools.go). selfBin is left empty:
// chat's own run_workflow tool is never adapted here (the native,
// engine-aware workflow_run supersedes it — see internal/mcp/tools.go), so
// MonoagentTools never needs to shell back into this binary. The instance is
// bound to its profile when it is built and keeps it. MonoagentTools's own
// mutable state is behind its own sync.RWMutex and its *sql.DB handle is safe
// for concurrent use, so sharing one instance per profile across concurrent
// tools/call goroutines is safe, same as rt.store/rt.registry.
func (rt *runtime) chatTools(profileID string) *aichat.MonoagentTools {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.chat == nil {
		rt.chat = map[string]*aichat.MonoagentTools{}
	}
	mt := rt.chat[profileID]
	if mt == nil {
		mt = aichat.NewMonoagentTools(rt.db.DB, "", profileID)
		mt.SetUnscopedLabel("unscoped:mcp") // the label of an org proposal made here: there is no conversation
		if rt.store != nil {
			mt.SetWorkflowStore(rt.store)
		}
		if rt.untrusted != "" {
			mt.MarkUntrusted(rt.untrusted)
		}
		rt.chat[profileID] = mt
	}
	return mt
}

// shareUntrusted carries the injection gate of one profile's chat tools to
// the others: when mt has seen untrusted content, all of the server's tools
// have.
func (rt *runtime) shareUntrusted(mt *aichat.MonoagentTools) {
	source, seen := mt.UntrustedContentSeen()
	if !seen {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.untrusted != "" {
		return
	}
	rt.untrusted = source
	for _, other := range rt.chat {
		other.MarkUntrusted(source)
	}
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// migrateProfilesToPerProfileKeys mirrors the wails app's per-profile vault
// migration (wails-app/app.go migrateProfilesToPerProfileLayout) and the
// CLI's (cmd/monoagentcli initDB): every profile's secrets and connection
// blobs are re-encrypted off the shared legacy key onto the profile's own
// key, before any vault use. Idempotent and cheap once fully migrated;
// non-fatal per profile.
func migrateProfilesToPerProfileKeys(ctx context.Context, db *sql.DB) {
	// Without a vault_keys_legacy table no row can be under the legacy key
	// (fresh install) — skip the per-profile loop entirely.
	var legacy int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vault_keys_legacy'`).Scan(&legacy); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: vault key migration: %v\n", err)
		return
	}
	if legacy == 0 {
		return
	}

	rows, err := db.QueryContext(ctx, `SELECT id FROM profiles`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: vault key migration: listing profiles: %v\n", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		if migrated, errs := secrets.MigrateProfileVaultKeys(ctx, db, id); migrated > 0 || len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "mcp: warning: profile %s: vault key migration: %v\n", id, e)
			}
		}
		if migrated, errs := connections.MigrateProfileBlobs(ctx, db, id); migrated > 0 || len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "mcp: warning: profile %s: connection data migration: %v\n", id, e)
			}
		}
	}
}

// runtime lazily bootstraps and caches the shared runtime for a server.
// It is race-safe: request handlers run on separate goroutines and may
// call it simultaneously on first use.
func (s *Server) runtime() (*runtime, error) {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()
	if s.rt != nil {
		return s.rt, nil
	}
	rt, err := newRuntime(s.opts)
	if err != nil {
		return nil, err
	}
	s.rt = rt
	return rt, nil
}

// Close releases the runtime's engine and database. Safe on nil receivers
// and safe to call more than once.
func (rt *runtime) Close() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.runStop != nil {
		rt.runStop()
	}
	for id, e := range rt.engines {
		_ = e.Stop()
		delete(rt.engines, id)
	}
	for id, sc := range rt.scheds {
		_ = sc.Stop()
		delete(rt.scheds, id)
	}
	if rt.db != nil {
		_ = rt.db.Close()
		rt.db = nil
	}
}

// newRuntime opens the database (applying migrations) and builds the hybrid
// workflow store. It resolves no profile: each call does.
func newRuntime(opts Options) (*runtime, error) {
	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = "~/.monoagent/monoagent.db"
	}
	dbPath = expandHome(dbPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("mcp: create db dir: %w", err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("mcp: open database: %w", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("mcp: apply migrations: %w", err)
	}

	// Best-effort vault migrations, mirroring the CLI's initDB. Grant mode
	// never touches the vault or keychain (C-22), so it skips them.
	ctx := context.Background()
	if opts.Grant != "" {
		return grantRuntime(db, opts)
	}
	if _, _, err := connections.MigrateConnectionsToVault(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: connections migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateFieldsToKV(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: vault key-value migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateSessionsToVault(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: sessions migration: %v\n", err)
	}
	if _, err := secrets.RetireAIProviderEntries(ctx, db.DB, filepath.Join(filepath.Dir(dbPath), "backups")); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: retiring AI provider keys: %v\n", err)
	}
	migrateProfilesToPerProfileKeys(ctx, db.DB)

	sqlStore := workflow.NewSQLiteWorkflowStore(db.DB)
	wfDir := opts.WorkflowsDir
	if wfDir == "" {
		wfDir = "~/.monoagent/workflows"
	}
	fileStore, err := workflow.NewWorkflowFileStore(expandHome(wfDir))
	if err != nil {
		fileStore = nil
	} else {
		_ = workflowio.StampFileWorkflows(ctx, db.DB, expandHome(wfDir))
	}

	store := workflow.NewHybridWorkflowStore(fileStore, sqlStore)
	store.SetOnSaved(func(ctx context.Context, id string) { raiseGrantTiers(ctx, db.DB, store.GetWorkflow, id) })
	return &runtime{
		db:    db,
		store: store,
	}, nil
}

// raiseGrantTiers re-derives the tiers of the org grants of a workflow this
// server just saved, as every save through the CLI's store does (#284): a
// workflow that gained an outbound node makes its grants irreversible. The
// org's JSON file is brought in line at its next reconcile.
func raiseGrantTiers(ctx context.Context, db *sql.DB, load orggrant.WorkflowLoader, workflowID string) {
	store := orggrant.NewStore(db)
	grants, err := store.GrantsForWorkflow(ctx, workflowID)
	if err != nil || len(grants) == 0 {
		return
	}
	raised, err := store.RaiseTiers(ctx, grants, load)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: warning: workflow %s: re-deriving grant tiers: %v\n", workflowID, err)
	}
	for _, r := range raised {
		fmt.Fprintf(os.Stderr, "mcp: notice: org %s: %s\n", r.Org, r)
	}
}

// ensureEngine starts the profile's WorkflowEngine (queue workers, scheduler,
// resume loop) on first use of that profile. Browser-session nodes are not
// wired here: an MCP server never launches Chrome; run those workflows via the
// CLI or daemon.
func (rt *runtime) ensureEngine(ctx context.Context, profileID string) (*workflow.WorkflowEngine, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if e := rt.engines[profileID]; e != nil {
		return e, nil
	}
	logger := logging.Zerolog() // the process's level (default warn) and format, on stderr

	registry := noderegistry.Build(rt.db.DB)
	nodes.SetGlobalCredentialStore(connections.NewStore(rt.db.DB))
	cfgStore := cfgpkg.ConfigStore(&cfgpkg.DBConfigStore{DB: rt.db})
	nodes.SetGlobalConfigMgr(&cfgpkg.ConfigManagerAdapter{
		Mgr: cfgpkg.NewConfigManager(expandHome("~/.monoagent/configs"), cfgStore, logger),
	})

	sched := scheduler.NewScheduler(logger)
	sched.Start()

	engine := workflow.NewWorkflowEngineWithStore(rt.store, rt.db.DB, sched, registry, workflow.EngineConfig{
		MaxConcurrent:  5,
		QueueCapacity:  1000,
		PruneInterval:  time.Hour,
		MaxExecHistory: 500,
		ProfileID:      profileID,
	}, logger)
	// The engine serves every later call of the profile, so it runs under the
	// runtime's own context, never the context of the call that started it.
	if rt.runCtx == nil {
		rt.runCtx, rt.runStop = context.WithCancel(context.Background())
	}
	if err := engine.Start(rt.runCtx); err != nil {
		sched.Stop()
		return nil, fmt.Errorf("mcp: start engine: %w", err)
	}

	if rt.engines == nil {
		rt.engines = map[string]*workflow.WorkflowEngine{}
		rt.scheds = map[string]*scheduler.Scheduler{}
	}
	if rt.registry == nil {
		rt.registry = registry
	}
	rt.scheds[profileID] = sched
	rt.engines[profileID] = engine
	return engine, nil
}

// registryOrDefault returns the node registry, building a DB-less one if
// the engine has not been started yet.
func (rt *runtime) registryOrDefault() *workflow.NodeTypeRegistry {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.registry != nil {
		return rt.registry
	}
	return noderegistry.Build(nil)
}
