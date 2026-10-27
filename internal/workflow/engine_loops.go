package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// webhookRetryLoop retries the webhook bind every 30s after a startup
// EADDRINUSE, so this engine takes over webhook serving once the process
// holding the port exits. Exits when the engine context is cancelled.
// Webhook routes live in the server's route map regardless of whether the
// listener is up, so registrations made while unbound are served once the
// bind succeeds.
func (e *WorkflowEngine) webhookRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.webhookServer.Start(); err == nil {
				e.logger.Info().Str("addr", e.webhookServer.addr).
					Msg("engine: webhook port acquired; serving webhooks now")
				return
			}
		}
	}
}

// staleRunningAge is how long an execution may stay RUNNING before the reaper
// considers it stale (its owning process crashed without cleanup).
const staleRunningAge = 24 * time.Hour

// staleReapLoop periodically fails executions stuck RUNNING for over 24h
// whose owning process is dead (or was this process, which cannot have a
// legitimately running execution that old at startup). Keeps zombie rows
// from masquerading as active runs forever.
func (e *WorkflowEngine) staleReapLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if account.Require(ctx) != nil { // a locked account runs no housekeeping either
				continue
			}
			dctx, cancel := dbCtx()
			if err := e.store.ReapStaleRunningExecutions(dctx, time.Now().UTC().Add(-staleRunningAge)); err != nil {
				e.logger.Warn().Err(err).Msg("engine: reap stale RUNNING executions")
			}
			cancel()
		}
	}
}

// resumeLoop periodically re-enqueues WAITING executions whose pause points
// (Human-in-Loop items) have all been resolved, so they continue from where
// they paused. This is what makes an approval — from the GUI or the CLI, in any
// process — actually resume the run, and lets paused runs survive a restart.
// Each tick also adopts unowned QUEUED executions (--no-wait persistence).
func (e *WorkflowEngine) resumeLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.resumeTick(ctx)
		}
	}
}

// resumeTick is one pass of resumeLoop. It first stops runs cancelled from
// another process. A locked account does neither of the other halves
// (spec D8: a locked daemon starts nothing new): adopting would claim queued
// rows and resuming would flip paused runs to QUEUED, where the gate in
// handleExecution would fail them for good. Left as they are, they run again
// once the account is valid.
func (e *WorkflowEngine) resumeTick(ctx context.Context) {
	e.stopCancelledElsewhere(ctx)
	if tickErr := account.Require(ctx); tickErr != nil {
		return
	}
	e.adoptQueuedExecutions(ctx)
	ids, err := e.store.ListResumableExecutions(ctx)
	if err != nil {
		e.logger.Warn().Err(err).Msg("engine: listing resumable executions")
		return
	}
	for _, id := range ids {
		if err := e.ResumeExecution(id); err != nil {
			e.logger.Warn().Err(err).Str("execution_id", id).Msg("engine: failed to resume execution")
		}
	}
}

// stopCancelledElsewhere cancels the runs this engine has in flight whose row
// another process marked CANCELLED. `workflow cancel` never signals the
// daemon, so for a run the daemon owns the row is all it changes; without
// this the run went on to its end and overwrote CANCELLED with its result.
func (e *WorkflowEngine) stopCancelledElsewhere(ctx context.Context) {
	for _, id := range e.queue.RunningIDs() {
		if st, err := e.store.GetExecutionStatus(ctx, id); err == nil && st == "CANCELLED" {
			e.queue.CancelWithCause(id, nil)
		}
	}
}

// adoptQueuedExecutions claims unowned QUEUED executions (no resume state;
// pid 0 — the rows `workflow run --no-wait` persists before exiting — or the
// pid of a process that died before starting them) and enqueues them
// locally, so a live engine (the daemon, or any sufficiently long-lived
// engine command) picks the run up. Each row is claimed with a
// CAS (pid 0 → self) so among engines sharing the DB exactly one adopts it.
//
// It piggybacks the resume loop rather than running synchronously in Start:
// a startup pass would let short-lived commands (e.g. `workflow activate`)
// adopt runs they cancel milliseconds later at exit; the first 3s tick is
// immediate enough for any persistent engine.
func (e *WorkflowEngine) adoptQueuedExecutions(ctx context.Context) {
	dctx, cancel := dbCtx()
	defer cancel()
	ids, err := e.store.ListAdoptableExecutions(dctx)
	if err != nil {
		e.logger.Warn().Err(err).Msg("engine: listing adoptable QUEUED executions")
		return
	}
	for _, id := range ids {
		exec, err := e.store.GetExecution(dctx, id)
		if err != nil || exec == nil {
			continue
		}
		// Leave executions of other profiles' workflows for a matching
		// engine (the daemon serves all profiles and takes them itself).
		if wf, werr := e.store.GetWorkflow(dctx, exec.WorkflowID); werr == nil && wf != nil {
			if perr := e.checkWorkflowProfile(wf); perr != nil {
				continue
			}
		}
		claimed, err := e.store.ClaimQueuedExecution(dctx, id)
		if err != nil {
			e.logger.Warn().Err(err).Str("execution_id", id).Msg("engine: claiming queued execution")
			continue
		}
		if !claimed {
			continue // another engine won the CAS
		}
		req := ExecutionRequest{
			WorkflowID:    exec.WorkflowID,
			ExecutionID:   id,
			TriggerType:   exec.TriggerType,
			TriggerNodeID: exec.TriggerNodeID,
			TriggerData:   exec.TriggerData,
		}
		if err := e.queue.Enqueue(req); err != nil {
			e.logger.Warn().Err(err).Str("execution_id", id).Msg("engine: adopted execution could not be enqueued")
			e.persistExecutionFinished(e.logger, id, "FAILED", "queue full")
			continue
		}
		e.logger.Info().Str("execution_id", id).Msg("engine: adopted unowned QUEUED execution")
	}
}

// ResumeExecution re-enqueues a paused (WAITING) execution. handleExecution
// reloads its persisted resume_state so RunExecution skips completed nodes and
// continues from the pause point.
func (e *WorkflowEngine) ResumeExecution(executionID string) error {
	// No ctx here (the callers are loops and receivers); Require reads memory only.
	if resumeErr := account.Require(context.Background()); resumeErr != nil {
		return resumeErr
	}
	dctx, cancel := dbCtx()
	defer cancel()
	exec, err := e.store.GetExecution(dctx, executionID)
	if err != nil {
		return fmt.Errorf("engine: resume execution: %w", err)
	}
	if exec == nil {
		return fmt.Errorf("engine: resume execution: %w", ErrExecutionNotFound)
	}
	if exec.Status != "WAITING" {
		return nil // already resumed or finished by another tick
	}
	// Atomically flip WAITING → QUEUED (preserves resume_state). The guarded
	// CAS ensures that among concurrent engines sharing the DB exactly one
	// resumes the execution — the losers get flipped=false and stop.
	flipped, err := e.store.ResumeWaitingExecution(dctx, executionID)
	if err != nil {
		return fmt.Errorf("engine: resume execution: %w", err)
	}
	if !flipped {
		return nil // another engine won the resume, or it's no longer WAITING
	}
	req := ExecutionRequest{
		WorkflowID:    exec.WorkflowID,
		ExecutionID:   executionID,
		TriggerType:   exec.TriggerType,
		TriggerNodeID: exec.TriggerNodeID,
		TriggerData:   exec.TriggerData,
	}
	if err := e.queue.Enqueue(req); err != nil {
		// Transient (queue full / closing): revert to WAITING preserving the
		// resume state so the next resume tick retries — never destroy the run.
		_ = e.store.SetExecutionWaiting(dctx, executionID, exec.ResumeState)
		return fmt.Errorf("engine: resume execution: %w", err)
	}
	return nil
}

// pruneLoop runs on the prune interval and culls old execution history for
// every workflow.
func (e *WorkflowEngine) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(e.pruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.runPrune(ctx)
		}
	}
}

// runPrune iterates over all workflows and prunes execution history.
// A locked account prunes nothing.
func (e *WorkflowEngine) runPrune(ctx context.Context) {
	if account.Require(ctx) != nil {
		return
	}
	workflows, err := e.listEngineWorkflows(ctx)
	if err != nil {
		e.logger.Warn().Err(err).Msg("engine: prune: failed to list workflows")
		return
	}
	for _, wf := range workflows {
		if err := e.store.PruneExecutions(ctx, wf.ID, e.maxExecHistory); err != nil {
			e.logger.Warn().Err(err).
				Str("workflow_id", wf.ID).
				Msg("engine: prune: failed to prune executions")
		}
	}
}

// reregisterTriggers loads all active workflows and activates their triggers.
// A locked account arms none (account_triggers.go).
func (e *WorkflowEngine) reregisterTriggers(ctx context.Context) error {
	if e.triggersLocked() {
		return nil
	}
	workflows, err := e.listEngineWorkflows(ctx)
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}

	var firstErr error
	for _, wf := range workflows {
		if !wf.IsActive {
			continue
		}
		// Load the full workflow (with nodes) for trigger registration.
		full, err := e.store.GetWorkflow(ctx, wf.ID)
		if err != nil {
			e.logger.Warn().Err(err).
				Str("workflow_id", wf.ID).
				Msg("engine: reregister triggers: failed to load workflow")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if full == nil || !full.IsActive {
			continue
		}
		if err := e.triggerMgr.ActivateWorkflow(ctx, full); err != nil {
			e.logger.Warn().Err(err).
				Str("workflow_id", wf.ID).
				Msg("engine: reregister triggers: failed to activate workflow triggers")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// listEngineWorkflows lists the workflows this engine looks after: its own
// profile's, or every profile's when it was built with AllowAllProfiles (the
// daemon). An engine with neither fails with ErrNoProfile.
func (e *WorkflowEngine) listEngineWorkflows(ctx context.Context) ([]Workflow, error) {
	if e.allowAllProfiles {
		return e.store.ListAllProfilesWorkflows(ctx)
	}
	return e.store.ListWorkflows(ctx, e.profileID)
}
