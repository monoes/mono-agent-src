// Package httpapi implements a read-first REST/JSON surface over the
// workflow engine and vault, for external agents that cannot speak the
// stdio MCP protocol. It mirrors internal/mcp's conventions: a lazily
// bootstrapped runtime (DB + profile + store, engine started on first
// mutating call), the same output-redaction pipeline
// (workflow.RedactAndTruncateItems), and loopback-only binding by default.
package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"net/http"

	"github.com/monoes/mono-agent/internal/cli/workflowio"
	cfgpkg "github.com/monoes/mono-agent/internal/config"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/scheduler"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Options configures the server. It mirrors mcp.Options. There is no
// profile option: the server serves every profile, and each request names the
// profile it is for in its path (/profiles/{profile}/...).
type Options struct {
	// DBPath is the SQLite database path ("~/..." expanded). Empty
	// defaults to ~/.monoagent/monoagent.db.
	DBPath string
	// WorkflowsDir overrides the JSON workflow file store directory
	// (default ~/.monoagent/workflows; mainly for tests).
	WorkflowsDir string
	// Addr is the listen address. Empty defaults to
	// MONOAGENT_HTTPAPI_ADDR, then "127.0.0.1:9322".
	Addr string
	// AllowMutations enables the mutating endpoints (workflow run/
	// activate/deactivate, hil approve/reject). Empty consults
	// MONOAGENT_HTTPAPI_ALLOW_MUTATIONS=="1".
	AllowMutations bool
	// DisableProfileBearer refuses the profile's own bearer token (the vault entry),
	// so only scoped agent tokens open the API. Empty consults
	// MONOAGENT_HTTPAPI_DISABLE_PROFILE_BEARER=="1".
	DisableProfileBearer bool
	// AllowedHosts are extra Host header values (host:port) the server answers to.
	// The loopback spellings of the bound port are always allowed; a server bound to
	// a non-loopback address serves other names only when they are listed here or in
	// MONOAGENT_HTTPAPI_ALLOWED_HOSTS (comma separated).
	AllowedHosts []string
	// Version is reported by /health.
	Version string

	// DB, Store, and Engine let a host process (`monoagentcli daemon`) serve
	// the API over its own database and already-running engine instead of
	// opening a second one: two engines in one process would race to adopt
	// the same queued executions. The host owns their lifecycle; Close
	// leaves them open.
	DB     *storage.Database
	Store  *workflow.HybridWorkflowStore
	Engine *workflow.WorkflowEngine

	// ExtraRoutes registers routes with their own authentication (the
	// automation-role endpoint receiver). They are registered whether or
	// not AllowMutations is set.
	ExtraRoutes func(mux *http.ServeMux)
}

// runtime holds the lazily-bootstrapped database, store, and (on the first
// mutating call for a profile) that profile's WorkflowEngine. It is a near-verbatim
// copy of internal/mcp's runtime — kept as its own unexported type here
// rather than shared, since internal/mcp does not export it and the two
// packages must stay independently buildable per the repo's internal/
// package boundaries.
type runtime struct {
	db       *storage.Database
	store    *workflow.HybridWorkflowStore
	registry *workflow.NodeTypeRegistry
	// engine is the host's engine (daemon): it serves every profile. A
	// standalone server has none and builds one engine per profile in engines,
	// each bound to its profile, on that profile's first mutating call.
	engine  *workflow.WorkflowEngine
	engines map[string]*profileEngine
	hosted  bool // DB and engine belong to the host process

	// runCtx outlives every request: the engines a standalone server starts
	// on a request's behalf run under it, and Close cancels it. An engine
	// started with the first request's context would die when that request
	// ended and leave later runs of the profile queued forever.
	runCtx  context.Context
	runStop context.CancelFunc

	// mu guards the lazy engine/registry/scheduler bootstrap: HTTP
	// requests are handled concurrently, so several mutating requests may
	// race to start the engine on first use.
	mu sync.Mutex
}

// profileEngine is a standalone server's engine for one profile, with the
// scheduler it owns.
type profileEngine struct {
	engine *workflow.WorkflowEngine
	sched  *scheduler.Scheduler
}

func orDefaultDBPath(p string) string {
	if p == "" {
		return "~/.monoagent/monoagent.db"
	}
	return p
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

// migrateProfilesToPerProfileKeys mirrors internal/mcp/engine.go's function
// of the same name (itself mirroring the CLI's initDB / the wails app's
// migration): every profile's secrets and connection blobs are re-encrypted
// off the shared legacy key onto the profile's own key, before any vault
// use. Idempotent and cheap once fully migrated; non-fatal per profile.
func migrateProfilesToPerProfileKeys(ctx context.Context, db *sql.DB) {
	var legacy int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vault_keys_legacy'`).Scan(&legacy); err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: vault key migration: %v\n", err)
		return
	}
	if legacy == 0 {
		return
	}

	rows, err := db.QueryContext(ctx, `SELECT id FROM profiles`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: vault key migration: listing profiles: %v\n", err)
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
				fmt.Fprintf(os.Stderr, "httpapi: warning: profile %s: vault key migration: %v\n", id, e)
			}
		}
		if migrated, errs := connections.MigrateProfileBlobs(ctx, db, id); migrated > 0 || len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "httpapi: warning: profile %s: connection data migration: %v\n", id, e)
			}
		}
	}
}

// newRuntime opens the database (applying migrations) and builds the hybrid
// workflow store. It picks no profile: every request names its own.
func newRuntime(opts Options) (*runtime, error) {
	if opts.DB != nil {
		store := opts.Store
		if store == nil {
			store = workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(opts.DB.DB))
		}
		return &runtime{db: opts.DB, store: store, engine: opts.Engine, hosted: true}, nil
	}
	dbPath := expandHome(orDefaultDBPath(opts.DBPath))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("httpapi: create db dir: %w", err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("httpapi: open database: %w", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("httpapi: apply migrations: %w", err)
	}

	ctx := context.Background()
	if _, _, err := connections.MigrateConnectionsToVault(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: connections migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateFieldsToKV(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: vault key-value migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateSessionsToVault(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: sessions migration: %v\n", err)
	}
	if _, err := secrets.RetireAIProviderEntries(ctx, db.DB, filepath.Join(filepath.Dir(dbPath), "backups")); err != nil {
		fmt.Fprintf(os.Stderr, "httpapi: warning: retiring AI provider keys: %v\n", err)
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

	runCtx, runStop := context.WithCancel(context.Background())
	return &runtime{
		db:      db,
		store:   workflow.NewHybridWorkflowStore(fileStore, sqlStore),
		runCtx:  runCtx,
		runStop: runStop,
	}, nil
}

// Close releases the runtime's engines and database. Safe on nil receivers
// and safe to call more than once.
func (rt *runtime) Close() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.hosted {
		rt.engine, rt.db = nil, nil
		return
	}
	if rt.runStop != nil {
		rt.runStop()
	}
	for id, pe := range rt.engines {
		_ = pe.engine.Stop()
		_ = pe.sched.Stop()
		delete(rt.engines, id)
	}
	if rt.db != nil {
		_ = rt.db.Close()
		rt.db = nil
	}
}

// engineFor returns the engine that runs profileID's workflows, starting it
// (queue workers, scheduler, resume loop) on first use. A host's engine (the
// daemon) serves every profile; a standalone server starts one engine per
// profile, each bound to its profile like the engine of a CLI run. The caller
// has already checked that the workflow belongs to profileID.
// Browser-session nodes are not wired here — like the MCP server, the HTTP API
// never launches Chrome; run those workflows via the CLI or daemon.
func (rt *runtime) engineFor(ctx context.Context, profileID string) (*workflow.WorkflowEngine, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.engine != nil {
		return rt.engine, nil
	}
	if pe, ok := rt.engines[profileID]; ok {
		return pe.engine, nil
	}
	logger := logging.Zerolog()

	if rt.registry == nil {
		rt.registry = noderegistry.Build(rt.db.DB)
	}
	nodes.SetGlobalCredentialStore(connections.NewStore(rt.db.DB))
	cfgStore := cfgpkg.ConfigStore(&cfgpkg.DBConfigStore{DB: rt.db})
	nodes.SetGlobalConfigMgr(&cfgpkg.ConfigManagerAdapter{
		Mgr: cfgpkg.NewConfigManager(expandHome("~/.monoagent/configs"), cfgStore, logger),
	})

	sched := scheduler.NewScheduler(logger)
	sched.Start()

	engine := workflow.NewWorkflowEngineWithStore(rt.store, rt.db.DB, sched, rt.registry, workflow.EngineConfig{
		MaxConcurrent:  5,
		QueueCapacity:  1000,
		PruneInterval:  time.Hour,
		MaxExecHistory: 500,
		ProfileID:      profileID,
	}, logger)
	if err := engine.Start(rt.runCtx); err != nil {
		sched.Stop()
		return nil, fmt.Errorf("httpapi: start engine: %w", err)
	}
	if rt.engines == nil {
		rt.engines = map[string]*profileEngine{}
	}
	rt.engines[profileID] = &profileEngine{engine: engine, sched: sched}
	return engine, nil
}

// registryOrDefault returns the node registry, building a DB-less one if
// no engine has been started yet.
func (rt *runtime) registryOrDefault() *workflow.NodeTypeRegistry {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.registry != nil {
		return rt.registry
	}
	return noderegistry.Build(nil)
}
