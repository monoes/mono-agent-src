package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/rs/zerolog"
)

// WorkflowEngine is the central coordinator for workflow management and execution.
// It owns the queue, trigger manager, webhook server, node registry, and store.
type WorkflowEngine struct {
	store            WorkflowStore
	connStore        *connections.Store
	registry         *NodeTypeRegistry
	queue            *ExecutionQueue
	triggerMgr       *TriggerManager
	webhookServer    *WebhookServer
	scheduler        SchedulerInterface
	expr             *ExpressionEngine
	logger           zerolog.Logger
	ctx              context.Context
	cancel           context.CancelFunc
	pruneInterval    time.Duration
	maxExecHistory   int
	profileID        string
	allowAllProfiles bool
	drops            dropNotes // triggers dropped while the account is locked
	reconciler       triggerReconciler
}

// EngineConfig holds WorkflowEngine configuration.
type EngineConfig struct {
	WebhookAddr    string        // e.g. ":9321"
	MaxConcurrent  int           // default 3, max 20
	QueueCapacity  int           // default 1000
	PruneInterval  time.Duration // default 1h
	MaxExecHistory int           // default 500
	ProfileID      string        // active profile for vault image registration
	// AllowAllProfiles disables the single-profile guard in
	// checkWorkflowProfile, letting this engine activate/run workflows
	// belonging to any profile. Intended for a long-running daemon process
	// that keeps every active workflow's triggers alive regardless of which
	// profile is selected in the UI.
	AllowAllProfiles bool
}

// NewWorkflowEngineWithStore creates a WorkflowEngine using a caller-supplied
// WorkflowStore. Use this when you need a HybridWorkflowStore (file store +
// SQLite). db is still required for the connections store.
func NewWorkflowEngineWithStore(store WorkflowStore, db *sql.DB, scheduler SchedulerInterface, registry *NodeTypeRegistry, cfg EngineConfig, logger zerolog.Logger) *WorkflowEngine {
	if err := applyEngineDefaults(&cfg); err != nil {
		// Invalid MONOAGENT_WEBHOOK_ADDR: applyEngineDefaults has already
		// fallen back to the config value / loopback default, so the engine
		// still starts — surfacing the error loudly rather than dying.
		logger.Error().Err(err).Msg("engine: webhook address override rejected, using config/default instead")
	}
	connStore := connections.NewStore(db)
	webhookServer := NewWebhookServer(cfg.WebhookAddr, logger)
	profileID := cfg.ProfileID
	if profileID == "" {
		profileID = "default"
	}
	e := &WorkflowEngine{
		store:            store,
		connStore:        connStore,
		registry:         registry,
		webhookServer:    webhookServer,
		scheduler:        scheduler,
		expr:             NewExpressionEngine(),
		logger:           logger,
		pruneInterval:    cfg.PruneInterval,
		maxExecHistory:   cfg.MaxExecHistory,
		profileID:        profileID,
		allowAllProfiles: cfg.AllowAllProfiles,
	}
	e.triggerMgr = NewTriggerManager(store, webhookServer, scheduler,
		func(workflowID string, nodeID string, items []Item) { e.handleTrigger(workflowID, nodeID, items) },
		logger,
	)
	e.queue = NewExecutionQueue(cfg.QueueCapacity, cfg.MaxConcurrent,
		func(ctx context.Context, req ExecutionRequest) { e.handleExecution(ctx, req) },
		logger,
	)
	e.queue.claim = e.claimLocal
	e.wireWebhookRuns()
	return e
}

// defaultWebhookAddr binds the webhook server to loopback so triggers aren't
// reachable from the LAN without auth; operators can opt into a wider bind
// via MONOAGENT_WEBHOOK_ADDR (e.g. "0.0.0.0:9321") or EngineConfig.WebhookAddr.
const defaultWebhookAddr = "127.0.0.1:9321"

// applyEngineDefaults fills zero-valued EngineConfig fields. WebhookAddr
// resolution order is env MONOAGENT_WEBHOOK_ADDR > cfg.WebhookAddr >
// defaultWebhookAddr. An env value that fails host:port validation returns an
// error and leaves the config/default value in place.
func applyEngineDefaults(cfg *EngineConfig) error {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
	}
	if cfg.MaxConcurrent > 20 {
		cfg.MaxConcurrent = 20
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 1000
	}
	if cfg.PruneInterval <= 0 {
		cfg.PruneInterval = time.Hour
	}
	if cfg.MaxExecHistory <= 0 {
		cfg.MaxExecHistory = 500
	}
	if cfg.WebhookAddr == "" {
		cfg.WebhookAddr = defaultWebhookAddr
	}
	if envAddr := strings.TrimSpace(os.Getenv("MONOAGENT_WEBHOOK_ADDR")); envAddr != "" {
		if _, _, err := net.SplitHostPort(envAddr); err != nil {
			return fmt.Errorf("invalid MONOAGENT_WEBHOOK_ADDR %q (want host:port, e.g. 0.0.0.0:9321): %w", envAddr, err)
		}
		cfg.WebhookAddr = envAddr
	}
	return nil
}

// Start initializes the engine: starts the queue workers, webhook server,
// recovers stale executions, re-registers triggers for active workflows,
// and starts the prune loop.
func (e *WorkflowEngine) Start(ctx context.Context) error {
	e.ctx, e.cancel = context.WithCancel(ctx)

	// 1. Start webhook server. The webhook port is a single fixed port shared by
	// every monoagent process, so a second engine (a CLI run started while the
	// daemon or the app is up) can never bind it. That must not stop the engine:
	// webhook deliveries are already being served by whoever owns the port, and
	// everything else — manual runs, schedules, node execution — is unaffected.
	// A background loop keeps retrying the bind so this engine takes over
	// webhook serving as soon as the current owner exits.
	if err := e.webhookServer.Start(); err != nil {
		if !isAddrInUse(err) {
			return fmt.Errorf("engine: start webhook server: %w", err)
		}
		e.logger.Warn().Err(err).Str("addr", e.webhookServer.addr).
			Msg("engine: webhook port already in use by another monoagent process — this engine will not serve webhooks yet, retrying every 30s, everything else runs normally")
		go e.webhookRetryLoop(e.ctx)
	}

	// 2. Start queue workers.
	e.queue.Start(e.ctx)

	// 3. Recover stale executions left over from a prior crash or restart.
	if err := e.store.RecoverStaleExecutions(e.ctx); err != nil {
		e.logger.Warn().Err(err).Msg("engine: failed to recover stale executions")
	}

	// 3a. Reject HIL items orphaned by a crash — their execution is no longer
	// running, so their in-process waiter is gone and approving them is a no-op.
	// This clears zombie 'pending' rows from the approvals queue.
	if db := e.store.RawDB(); db != nil {
		if _, err := db.ExecContext(e.ctx,
			`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
			 WHERE status='pending' AND execution_id IN (
			     SELECT id FROM workflow_executions WHERE status NOT IN ('RUNNING','QUEUED','WAITING'))`,
		); err != nil {
			e.logger.Warn().Err(err).Msg("engine: failed to clean up orphaned HIL items")
		}
	}

	// 4. Re-register triggers for all active workflows in this engine's
	// profile — but only for a long-running daemon-style engine.
	// AllowAllProfiles is already the documented signal for "this is the
	// long-running daemon process that keeps every active workflow's
	// triggers alive" (see EngineConfig.AllowAllProfiles); reuse it here
	// rather than adding a second flag with the same meaning. Without this
	// guard, every one-shot engine.Start() call (`workflow run`, `activate`,
	// `deactivate`, `delete`, `templates run` — none of which need any
	// workflow's trigger *other* than the one they explicitly
	// activate/deactivate themselves) would re-register every other active
	// workflow's schedule/webhook trigger into its own short-lived
	// TriggerManager and robfig/cron scheduler instance. Each
	// scheduler instance is fully process-local with no cross-process
	// guard for schedule ticks, so if a tick landed while both the daemon
	// and the one-shot process had the same trigger registered, both would
	// independently create an execution for it — a real-world duplicate
	// side effect for whatever the workflow does.
	if e.allowAllProfiles {
		if err := e.reregisterTriggers(e.ctx); err != nil {
			e.logger.Warn().Err(err).Msg("engine: failed to re-register some triggers on startup")
		}
	}

	// 5. Start prune, resume, and stale-RUNNING reaper loops.
	go e.pruneLoop(e.ctx)
	go e.resumeLoop(e.ctx)
	go e.staleReapLoop(e.ctx)

	// 6. Cancel what is in flight when monoes.me refuses the account (spec
	// section 6.4). Registered here, so every process that runs an engine has it.
	e.watchAccount()

	e.logger.Info().Msg("workflow engine started")
	return nil
}

// Stop gracefully shuts down all components.
func (e *WorkflowEngine) Stop() error {
	e.logger.Info().Msg("workflow engine stopping")

	// Signal all goroutines driven by e.ctx.
	if e.cancel != nil {
		e.cancel()
	}

	// Deactivate all triggers (stops cron jobs and deregisters webhooks).
	e.triggerMgr.DeactivateAll()

	// Stop the queue — drains in-flight work with a bounded 15s grace period
	// (handlers that ignore cancellation past the grace are abandoned).
	e.queue.Stop()

	// Shut down the webhook HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := e.webhookServer.Stop(shutdownCtx); err != nil {
		e.logger.Warn().Err(err).Msg("engine: webhook server shutdown error")
	}

	e.logger.Info().Msg("workflow engine stopped")
	return nil
}

// Scheduler returns the scheduler this engine was built with (may be nil in
// tests). Long-lived processes (the daemon) should Stop it after engine Stop.
func (e *WorkflowEngine) Scheduler() SchedulerInterface { return e.scheduler }

// ScheduledRuns lists the registered schedule triggers and their next fire time.
func (e *WorkflowEngine) ScheduledRuns() []ScheduledRun {
	if e.triggerMgr == nil {
		return nil
	}
	return e.triggerMgr.ScheduledRuns()
}

// SetTraceAdmitter lets webhook runs continue a chain from a signed
// X-Monoagent-Trace header, recording each such crossing through fn (see
// WebhookServer.SetTraceAdmitter). The daemon wires it to the org ledger.
func (e *WorkflowEngine) SetTraceAdmitter(fn TraceAdmitter) {
	e.webhookServer.SetTraceAdmitter(fn)
}
