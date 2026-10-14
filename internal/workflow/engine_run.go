package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/rs/zerolog"
)

// handleTrigger is called by TriggerManager whenever a trigger fires.
// It creates a WorkflowExecution record and enqueues it for execution.
func (e *WorkflowEngine) handleTrigger(workflowID string, nodeID string, items []Item) {
	ctx := e.ctx

	// A locked account starts nothing: the trigger is dropped, and a schedule's
	// tick is not made up later. One log line a minute says so (spec section
	// 6.2). The judgement uses its own context: e.ctx is nil before Start and
	// cancelled during Stop, and a trigger that fires then is still a trigger.
	if dropErr := account.Require(context.Background()); dropErr != nil {
		if n, due := e.drops.note(); due {
			e.logger.Warn().Err(dropErr).Int("dropped", n).Str("workflow_id", workflowID).
				Msg("engine: monoes.me login required; trigger dropped (logged once a minute)")
		}
		return
	}

	// Determine trigger type from the node.  We load the workflow to find the
	// node's Type field.
	triggerType := "unknown"
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		e.logger.Error().Err(err).
			Str("workflow_id", workflowID).
			Msg("engine: handleTrigger: failed to load workflow, skipping trigger")
		return
	}
	if wf == nil {
		e.logger.Error().
			Str("workflow_id", workflowID).
			Msg("engine: handleTrigger: workflow not found, skipping trigger")
		return
	}
	if !wf.IsActive {
		e.logger.Warn().
			Str("workflow_id", workflowID).
			Msg("engine: handleTrigger: workflow is not active, ignoring trigger")
		return
	}
	for _, n := range wf.Nodes {
		if n.ID == nodeID {
			triggerType = n.Type
			break
		}
	}

	// Build trigger data from the first item (if any).
	triggerData := make(map[string]interface{})
	if len(items) > 0 && items[0].JSON != nil {
		triggerData = items[0].JSON
	}

	exec := &WorkflowExecution{
		WorkflowID:    workflowID,
		ProfileID:     executionProfileID(wf, e.profileID),
		Status:        "QUEUED",
		TriggerType:   triggerType,
		TriggerNodeID: nodeID,
		TriggerData:   triggerData,
		PID:           ownerPID(),
	}

	if err := e.store.CreateExecution(ctx, exec); err != nil {
		e.logger.Error().Err(err).
			Str("workflow_id", workflowID).
			Str("node_id", nodeID).
			Msg("engine: handleTrigger: failed to create execution record")
		return
	}

	req := ExecutionRequest{
		WorkflowID:    workflowID,
		ExecutionID:   exec.ID,
		TriggerType:   triggerType,
		TriggerNodeID: nodeID,
		TriggerData:   triggerData,
	}

	if err := e.queue.Enqueue(req); err != nil {
		// Queue is full — mark the execution as FAILED immediately.
		e.logger.Warn().
			Str("execution_id", exec.ID).
			Str("workflow_id", workflowID).
			Msg("engine: handleTrigger: queue full, execution will not run")
		if updateErr := e.store.SetExecutionFinished(ctx, exec.ID, "FAILED", "queue full"); updateErr != nil {
			e.logger.Error().Err(updateErr).
				Str("execution_id", exec.ID).
				Msg("engine: handleTrigger: failed to mark execution as FAILED after queue full")
		}
	}
}

// handleExecution is called by a queue worker for each execution request.
func (e *WorkflowEngine) handleExecution(ctx context.Context, req ExecutionRequest) {
	log := e.logger.With().
		Str("execution_id", req.ExecutionID).
		Str("workflow_id", req.WorkflowID).
		Logger()

	// 0. Claim the run: QUEUED → RUNNING, guarded. A cancel that landed while
	// the request sat in the queue, another engine, or a second dispatch of
	// the same execution makes the claim fail, and then this dispatch must
	// not run anything — that is what keeps an execution body from running
	// twice.
	if started, err := e.startQueued(context.WithoutCancel(ctx), req.ExecutionID); err != nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to mark execution as RUNNING")
		return
	} else if !started {
		st, _ := e.store.GetExecutionStatus(ctx, req.ExecutionID)
		log.Info().Str("status", st).Msg("engine: handleExecution: execution not QUEUED for this process (cancelled, or dispatched elsewhere); skipping")
		return
	}

	// 0b. A locked account starts nothing (spec section 6.2). Record the refusal
	// and stop before the workflow is loaded; the row is final, nothing retries it.
	if lrErr := account.Require(ctx); lrErr != nil {
		log.Warn().Err(lrErr).Msg("engine: handleExecution: monoes.me login required; execution refused")
		e.persistExecutionFinished(log, req.ExecutionID, "FAILED", "login_required: "+lrErr.Error())
		return
	}

	// Persistence contexts are detached AND non-expiring, created fresh per
	// call: a deadline minted at dispatch (the old 10s persistCtx) expired
	// under any run longer than 10s, so the final SetExecutionFinished
	// silently failed and the row stayed RUNNING forever (zombie execution;
	// the CLI wait hung and the next startup marked it FAILED).
	persistCtx := func() context.Context { return context.WithoutCancel(ctx) }

	// 1. Load the workflow — the version this execution is pinned to.
	wf, err := e.loadExecutionWorkflow(ctx, log, req)
	if err != nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to load workflow")
		e.persistExecutionFinished(log, req.ExecutionID, "FAILED", fmt.Sprintf("load workflow: %s", err.Error()))
		return
	}
	if wf == nil {
		log.Error().Msg("engine: handleExecution: workflow not found")
		e.persistExecutionFinished(log, req.ExecutionID, "FAILED", "workflow not found")
		return
	}

	// 3. Build the DAG.
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to build DAG")
		e.persistExecutionFinished(log, req.ExecutionID, "FAILED", fmt.Sprintf("build dag: %s", err.Error()))
		return
	}

	// Load the full execution record (with trigger data) so runExecution has it.
	exec, err := e.store.GetExecution(persistCtx(), req.ExecutionID)
	if err != nil || exec == nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to load execution record")
		e.persistExecutionFinished(log, req.ExecutionID, "FAILED", "could not load execution record")
		return
	}
	// The row carries the trigger node that fired (RunExecution fires only
	// its branch); a request naming one wins for rows written before the
	// column existed.
	if req.TriggerNodeID != "" {
		exec.TriggerNodeID = req.TriggerNodeID
	}

	// 4. Execute via runExecution (defined in execution.go).
	runErr := e.runExecution(ctx, exec, wf, dag)

	// 5. On completion: update execution status to SUCCESS or FAILED.
	// persistExecutionFinished writes with a detached, non-expiring context
	// so the terminal status lands even if the execution context was
	// cancelled (engine shutdown) — and never discards a failure silently.
	var partialErr *PartialFailureError
	switch {
	case runErr == nil:
		log.Info().Msg("engine: execution finished successfully")
		e.persistExecutionFinished(log, req.ExecutionID, "SUCCESS", "")
	case errors.Is(runErr, ErrExecutionPaused):
		// The run suspended at a pause point (e.g. Human-in-Loop). RunExecution
		// already persisted WAITING + resume state; leave it for the resume loop.
		log.Info().Msg("engine: execution paused, awaiting resume")
	case errors.As(runErr, &partialErr):
		// The run completed but nodes failed under on_error=continue/skip/error_branch.
		// Surface that rather than a misleading green SUCCESS.
		log.Warn().Err(runErr).Msg("engine: execution finished with non-fatal node failures")
		e.persistExecutionFinished(log, req.ExecutionID, "SUCCESS_WITH_ERRORS", runErr.Error())
	case errors.Is(runErr, ErrExecutionCancelled):
		log.Warn().Err(runErr).Str("final_status", "CANCELLED").Msg("engine: execution cancelled")
		e.persistExecutionFinished(log, req.ExecutionID, "CANCELLED", cancelledMessage(ctx, runErr))
	default:
		log.Warn().Err(runErr).Str("final_status", "FAILED").Msg("engine: execution finished with error")
		e.persistExecutionFinished(log, req.ExecutionID, failureStatus(runErr), runErr.Error())
	}
}

// persistExecutionFinished records an execution's terminal status using a
// detached, non-expiring context (WithoutCancel, fresh per call), and does
// not give up silently: a lost final-status write leaves the row RUNNING
// forever — the CLI wait hangs and the next startup's recovery marks it
// FAILED. A failed write is logged at error level and retried once.
func (e *WorkflowEngine) persistExecutionFinished(log zerolog.Logger, executionID, status, errMsg string) {
	defer e.onExecutionFinished(executionID) // wakes a parent waiting on this sub-workflow run
	defer e.afterExecutionFinished(log, executionID, status, errMsg)
	persistCtx := context.WithoutCancel(context.Background())
	err := e.store.SetExecutionFinished(persistCtx, executionID, status, errMsg)
	if err == nil {
		return
	}
	log.Error().Err(err).
		Str("execution_id", executionID).
		Str("status", status).
		Msg("engine: failed to persist final execution status — retrying once")
	time.Sleep(250 * time.Millisecond)
	if err = e.store.SetExecutionFinished(persistCtx, executionID, status, errMsg); err != nil {
		log.Error().Err(err).
			Str("execution_id", executionID).
			Str("status", status).
			Msg("engine: final execution status could not be persisted — the row may stay RUNNING and be recovered as FAILED on next startup")
	}
}

// ---------------------------------------------------------------------------
// Execution management
// ---------------------------------------------------------------------------

// TriggerWorkflow manually triggers a workflow (for manual trigger nodes).
// Returns the new execution ID. A locked account creates no execution row: the
// caller gets the typed *account.LoginRequiredError.
func (e *WorkflowEngine) TriggerWorkflow(ctx context.Context, workflowID string, data map[string]interface{}) (string, error) {
	if trigErr := account.Require(ctx); trigErr != nil {
		return "", trigErr
	}
	exec, err := e.newManualExecution(ctx, workflowID, data, ownerPID())
	if err != nil {
		return "", err
	}

	req := ExecutionRequest{
		WorkflowID:  workflowID,
		ExecutionID: exec.ID,
		TriggerType: "trigger.manual",
		TriggerData: exec.TriggerData,
	}

	if err := e.queue.Enqueue(req); err != nil {
		e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("engine: trigger workflow: %w", ErrQueueFull)
	}

	e.logger.Info().
		Str("workflow_id", workflowID).
		Str("execution_id", exec.ID).
		Msg("engine: manual trigger queued")
	return exec.ID, nil
}

// TriggerWorkflowPersistOnly creates a QUEUED execution for workflowID
// WITHOUT enqueueing it locally. This backs `workflow run --no-wait`: the
// calling CLI exits right after, so a local enqueue would be cancelled with
// the process mid-run; instead the unowned row (pid 0) is adopted and run by
// whichever engine is alive — see adoptQueuedExecutions. Returns the new
// execution ID.
func (e *WorkflowEngine) TriggerWorkflowPersistOnly(ctx context.Context, workflowID string, data map[string]interface{}) (string, error) {
	if persistErr := account.Require(ctx); persistErr != nil {
		return "", persistErr
	}
	exec, err := e.newManualExecution(ctx, workflowID, data, 0)
	if err != nil {
		return "", err
	}
	e.logger.Info().
		Str("workflow_id", workflowID).
		Str("execution_id", exec.ID).
		Msg("engine: manual trigger persisted (unowned QUEUED, awaiting adoption)")
	return exec.ID, nil
}

// newManualExecution validates the workflow and persists a QUEUED execution
// row for a manual trigger, owned by pid (0: left for adoption). Shared by
// TriggerWorkflow and TriggerWorkflowPersistOnly.
func (e *WorkflowEngine) newManualExecution(ctx context.Context, workflowID string, data map[string]interface{}, pid int) (*WorkflowExecution, error) {
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("engine: trigger workflow: %w", err)
	}
	if wf == nil {
		return nil, fmt.Errorf("engine: trigger workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return nil, fmt.Errorf("engine: trigger workflow: %w", err)
	}
	if !wf.IsActive {
		return nil, fmt.Errorf("engine: trigger workflow: %w", ErrWorkflowInactive)
	}

	if data == nil {
		data = make(map[string]interface{})
	}

	exec := &WorkflowExecution{
		WorkflowID:  workflowID,
		ProfileID:   executionProfileID(wf, e.profileID),
		Status:      "QUEUED",
		TriggerType: "trigger.manual",
		TriggerData: data,
		PID:         pid,
	}

	if err := e.store.CreateExecution(ctx, exec); err != nil {
		return nil, fmt.Errorf("engine: trigger workflow: create execution: %w", err)
	}
	return exec, nil
}

// CancelExecution signals an in-flight execution to stop.
func (e *WorkflowEngine) CancelExecution(executionID string) {
	e.cancelExecution(executionID, nil)
}

// cancelExecution is CancelExecution with the cause of the cancel, which the
// running execution reads from its context (context.Cause).
func (e *WorkflowEngine) cancelExecution(executionID string, cause error) {
	// Authoritatively cancel a still-queued execution so it doesn't run once a
	// worker picks it up (queued requests have no cancel func yet). handleExecution
	// re-checks status before running, so this makes the cancel stick.
	dctx, cancel := dbCtx()
	defer cancel()
	// Only a run that has not finished takes its sub-workflow children with
	// it; a finished run's fire-and-forget children are left alone.
	status, _ := e.store.GetExecutionStatus(dctx, executionID)
	// Covers QUEUED (not yet dispatched) and WAITING (paused at a HIL node with
	// no live goroutine) — a status flip is enough for both.
	if cancelled, err := e.store.CancelQueuedExecution(dctx, executionID); err == nil && cancelled {
		// Reject any still-pending HIL items so a cancelled paused run can't be
		// resumed and doesn't leave zombie items in the approvals queue.
		if db := e.store.RawDB(); db != nil {
			_, _ = db.ExecContext(dctx,
				`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='pending'`,
				executionID)
		}
		e.logger.Info().Str("execution_id", executionID).Msg("engine: queued/waiting execution cancelled")
		e.onExecutionFinished(executionID) // wakes a parent waiting on this sub-workflow run
	}
	// Signal cancellation for an already-dispatched (running) execution.
	e.queue.CancelWithCause(executionID, cause)
	e.logger.Info().Str("execution_id", executionID).Msg("engine: execution cancel requested")
	if status != "" && !isTerminalStatus(status) {
		e.cancelSubWorkflowChildren(executionID, cause)
	}
}

// CancelRunning asks every execution this engine has dispatched and not
// finished to stop, through CancelExecution, and returns how many it asked.
// The account guard's refusal handler calls it (spec section 6.4), so each run
// is cancelled with errAccountRefused as its cause. A WAITING run has no
// goroutine and is left as it is.
func (e *WorkflowEngine) CancelRunning() int {
	ids := e.queue.RunningIDs()
	for _, id := range ids {
		e.cancelExecution(id, errAccountRefused)
	}
	return len(ids)
}

// RetryExecution re-queues a failed execution as a new execution.
func (e *WorkflowEngine) RetryExecution(ctx context.Context, executionID string) (string, error) {
	if retryErr := account.Require(ctx); retryErr != nil {
		return "", retryErr
	}
	orig, err := e.store.GetExecution(ctx, executionID)
	if err != nil {
		return "", fmt.Errorf("engine: retry execution: %w", err)
	}
	if orig == nil {
		return "", fmt.Errorf("engine: retry execution: %w", ErrExecutionNotFound)
	}
	// A retry copies the original trigger type and data, so an org_tool or
	// org_message run would start its automation again without a ledger
	// row and outside the grant's per-run and per-day caps (#127).
	switch orig.TriggerType {
	case TriggerTypeOrgTool, TriggerTypeOrgMessage:
		return "", fmt.Errorf("engine: retry execution %s: %w", executionID, ErrRetryOrgStarted)
	}

	wf, err := e.store.GetWorkflow(ctx, orig.WorkflowID)
	if err != nil {
		return "", fmt.Errorf("engine: retry execution: load workflow: %w", err)
	}
	if wf == nil {
		return "", fmt.Errorf("engine: retry execution: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return "", fmt.Errorf("engine: retry execution: %w", err)
	}

	// A webhook run's trace was admitted through the ledger when the
	// request came in (see WebhookServer.SetTraceAdmitter). A retry is not,
	// so it starts a fresh chain rather than continue that one uncounted.
	data := orig.TriggerData
	if orig.TriggerType == TriggerNodeTypeWebhook {
		data = withoutTrace(data, WebhookTraceKey)
	}
	exec := &WorkflowExecution{
		WorkflowID:  orig.WorkflowID,
		ProfileID:   executionProfileID(wf, e.profileID),
		Status:      "QUEUED",
		TriggerType: orig.TriggerType,
		// The retry replays the same trigger, so it fires the same branch.
		TriggerNodeID: orig.TriggerNodeID,
		TriggerData:   data,
		PID:           ownerPID(),
	}

	if err := e.store.CreateExecution(ctx, exec); err != nil {
		return "", fmt.Errorf("engine: retry execution: create new execution: %w", err)
	}

	req := ExecutionRequest{
		WorkflowID:    orig.WorkflowID,
		ExecutionID:   exec.ID,
		TriggerType:   orig.TriggerType,
		TriggerNodeID: orig.TriggerNodeID,
		TriggerData:   data,
	}

	if err := e.queue.Enqueue(req); err != nil {
		e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("engine: retry execution: %w", ErrQueueFull)
	}

	e.logger.Info().
		Str("original_execution_id", executionID).
		Str("new_execution_id", exec.ID).
		Msg("engine: execution retry queued")
	return exec.ID, nil
}

// runExecution delegates to RunExecution defined in execution.go.
func (e *WorkflowEngine) runExecution(ctx context.Context, exec *WorkflowExecution, wf *Workflow, dag *DAG) error {
	ctx = vault.ContextWithDB(ctx, e.store.RawDB())
	// Use the execution's own profile ID (stamped via executionProfileID at
	// creation time from the owning workflow), not the engine's — a single
	// engine instance can be running executions for many different profiles
	// at once (AllowAllProfiles). Fall back to the engine's profile only for
	// a legacy execution row with no ProfileID of its own.
	profileID := exec.ProfileID
	if profileID == "" {
		profileID = e.profileID
	}
	ctx = vault.ContextWithProfileID(ctx, profileID)
	ctx = WithSubWorkflowRunner(ctx, e)
	return RunExecution(ctx, exec, wf, dag, e.registry, e.store, e.connStore, e.expr, e.logger)
}
