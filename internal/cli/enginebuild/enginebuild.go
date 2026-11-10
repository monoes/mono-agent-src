// Package enginebuild builds the workflow engine the CLI commands run: the
// node registry, the browser and credential wiring the nodes read, the
// scheduler, and the workflow store, over the profile's database. The
// `daemon` and the `workflow` commands share it.
package enginebuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	cfgpkg "github.com/monoes/mono-agent/internal/config"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/scheduler"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Options are the global flags the engine depends on.
type Options struct {
	Verbose bool
	// ProfileID is the profile the engine runs as. It is read after OpenDB,
	// which resolves --profile (an id or a name, or the active profile).
	ProfileID        func() string
	AllowAllProfiles bool
}

// SessionProvider is the browser session provider the engine's browser nodes
// use; Close is called by the cleanup Build returns.
type SessionProvider interface {
	nodes.SessionProvider
	Close()
}

// Deps are the CLI's pieces the engine is wired with.
type Deps struct {
	OpenDB func() (*storage.Database, error)
	// NewSessionProvider builds the browser session provider for the engine's
	// logger and default profile.
	NewSessionProvider func(logger zerolog.Logger, defaultProfile string) SessionProvider
	BotRegistry        nodes.BotRegistry
	OpenWorkflowStore  func(*storage.Database) *workflow.HybridWorkflowStore
}

// Build constructs a fully wired WorkflowEngine suitable for CLI use.
// It creates its own scheduler (no action executor or store needed for workflow triggers).
// The returned cleanup func closes the browser session provider and the node
// health observers, then the database; callers should defer it alongside engine.Stop().
func Build(opts Options, deps Deps) (*workflow.WorkflowEngine, func(), error) {
	db, err := deps.OpenDB()
	if err != nil {
		return nil, func() {}, fmt.Errorf("open database: %w", err)
	}
	profileID := opts.ProfileID()

	registry := noderegistry.Build(db.DB)

	logger := logging.Zerolog()
	if opts.Verbose {
		logger = logger.Level(zerolog.TraceLevel)
	}

	// Set up browser session provider, bot registry, and config manager
	// so browser/social nodes work in workflows. Chrome extension only —
	// no Rod/Chromium fallback, sharing another local process's connection
	// when one already exists (e.g. the daemon). The bridge is connected
	// lazily on the first browser-node GetPage: connecting eagerly blocked
	// every engine command (workflow run/activate/daemon) for up to ~60s
	// and launched the user's real Chrome even for browserless workflows.
	extLogger := logger.With().Str("component", "extension").Logger()
	sessionProvider := deps.NewSessionProvider(extLogger, profileID)
	nodes.SetGlobalSessionProvider(sessionProvider)
	nodes.SetGlobalBotRegistry(deps.BotRegistry)
	nodes.SetGlobalCredentialStore(connections.NewStore(db.DB))

	cfgLogger := logging.Zerolog()
	cfgStore := cfgpkg.ConfigStore(&cfgpkg.DBConfigStore{DB: db})
	rawCfgMgr := cfgpkg.NewConfigManager(expandPath("~/.monoagent/configs"), cfgStore, cfgLogger)
	nodes.SetGlobalConfigMgr(&cfgpkg.ConfigManagerAdapter{Mgr: rawCfgMgr})

	sched := scheduler.NewScheduler(logger)
	sched.Start()

	engCfg := workflow.EngineConfig{
		MaxConcurrent:    5,
		QueueCapacity:    1000,
		PruneInterval:    time.Hour,
		MaxExecHistory:   500,
		ProfileID:        profileID,
		AllowAllProfiles: opts.AllowAllProfiles,
	}

	hybridStore := deps.OpenWorkflowStore(db)
	engine := workflow.NewWorkflowEngineWithStore(hybridStore, db.DB, sched, registry, engCfg, logger)
	return engine, func() {
		sessionProvider.Close()
		nodes.CloseHealthObservers()
		db.Close() //nolint:errcheck // the command is over; a held file blocks removing its home on Windows
	}, nil
}

// expandPath expands ~ to the user's home directory (main's helper).
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
