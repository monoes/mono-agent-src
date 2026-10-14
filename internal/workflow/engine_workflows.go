package workflow

import (
	"context"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Workflow lifecycle
// ---------------------------------------------------------------------------

// CreateWorkflow saves a new workflow (inactive by default).
func (e *WorkflowEngine) CreateWorkflow(ctx context.Context, w *Workflow) error {
	w.IsActive = false
	if w.ProfileID == "" {
		w.ProfileID = e.profileID
	}
	if err := e.store.CreateWorkflow(ctx, w); err != nil {
		return fmt.Errorf("engine: create workflow: %w", err)
	}
	if len(w.Nodes) > 0 {
		if err := e.store.SaveWorkflowNodes(ctx, w.ID, w.Nodes); err != nil {
			return fmt.Errorf("engine: create workflow nodes: %w", err)
		}
	}
	if len(w.Connections) > 0 {
		if err := e.store.SaveWorkflowConnections(ctx, w.ID, w.Connections); err != nil {
			return fmt.Errorf("engine: create workflow connections: %w", err)
		}
	}
	e.logger.Info().Str("workflow_id", w.ID).Str("name", w.Name).Msg("engine: workflow created")
	return nil
}

// SaveWorkflow updates a workflow's definition (nodes + connections) in one
// all-or-nothing store write; the active flag is the stored one (callers
// control it via Activate/DeactivateWorkflow). Triggers are touched only
// after the write committed: an active workflow's are swapped for the new
// definition's, and if those fail to register the previous definition is
// saved back and its triggers re-armed, so a failed save never leaves the
// workflow inactive or half-changed.
func (e *WorkflowEngine) SaveWorkflow(ctx context.Context, w *Workflow) error {
	existing, err := e.store.GetWorkflow(ctx, w.ID)
	if err != nil {
		return fmt.Errorf("engine: save workflow: load existing: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("engine: save workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(existing); err != nil {
		return fmt.Errorf("engine: save workflow: %w", err)
	}

	w.IsActive = existing.IsActive
	if err := e.saveDefinition(ctx, w); err != nil {
		return fmt.Errorf("engine: save workflow: %w", err)
	}

	if existing.IsActive {
		if err := e.rearmTriggers(ctx, w.ID); err != nil {
			restore := *existing
			if rerr := e.saveDefinition(ctx, &restore); rerr != nil {
				return fmt.Errorf("engine: save workflow: register triggers: %w (restoring the previous definition also failed: %v)", err, rerr)
			}
			if aerr := e.rearmTriggers(ctx, w.ID); aerr != nil {
				e.logger.Error().Err(aerr).Str("workflow_id", w.ID).Msg("engine: save workflow: previous triggers could not be re-armed")
			}
			return fmt.Errorf("engine: save workflow: register triggers: %w (the previous definition was kept)", err)
		}
	}

	e.logger.Info().Str("workflow_id", w.ID).Int("version", w.Version).Msg("engine: workflow saved")
	return nil
}

// executionProfileID resolves the profile to stamp on a new WorkflowExecution:
// the owning workflow's own ProfileID, since a single long-running daemon
// process's engine (RestoreActiveWorkflows) can be handling triggers for
// many profiles' workflows at once — engineProfileID alone would be wrong
// for anything but whichever profile the engine happened to be constructed
// with. Falls back to engineProfileID only for a legacy workflow row with no
// ProfileID of its own (handleTrigger skips the run entirely when the
// workflow can't be loaded, so wf is never nil here).
func executionProfileID(wf *Workflow, engineProfileID string) string {
	if wf != nil && wf.ProfileID != "" {
		return wf.ProfileID
	}
	return engineProfileID
}

// checkWorkflowProfile returns an error if wf belongs to a different profile
// than the engine's active profile, preventing cross-profile access to a
// workflow whose ID was guessed or leaked from another profile.
func (e *WorkflowEngine) checkWorkflowProfile(wf *Workflow) error {
	if e.allowAllProfiles {
		return nil
	}
	// A workflow with no profile id is the default profile's, the rule the
	// store's SQLite half and `workflow list` apply. Treating it as
	// everyone's let any profile run it and tag the run as its own.
	owner := wf.ProfileID
	if owner == "" {
		owner = "default"
	}
	if owner != e.profileID {
		return fmt.Errorf("workflow belongs to a different profile")
	}
	return nil
}

// RestoreActiveWorkflows re-activates every workflow across all profiles
// that is marked active in the store, registering their triggers with this
// engine. Intended to be called once after Start() by a long-running daemon
// process, so scheduled/webhook triggers survive process restarts. Requires
// AllowAllProfiles (set via EngineConfig) since it spans every profile.
//
// It then fires the schedule ticks missed while no daemon ran, for schedule
// nodes that opted into catch_up, and starts the trigger reconcile loop that
// keeps the registrations in step with changes other processes make.
func (e *WorkflowEngine) RestoreActiveWorkflows(ctx context.Context) error {
	if !e.allowAllProfiles {
		return fmt.Errorf("engine: RestoreActiveWorkflows requires AllowAllProfiles")
	}
	workflows, err := e.store.ListWorkflows(ctx, "")
	if err != nil {
		return fmt.Errorf("engine: restore active workflows: list: %w", err)
	}
	var restored, failed, caughtUp int
	for _, wf := range workflows {
		if !wf.IsActive {
			continue
		}
		full, err := e.store.GetWorkflow(ctx, wf.ID)
		if err != nil || full == nil {
			failed++
			e.logger.Warn().Str("workflow_id", wf.ID).Err(err).Msg("engine: restore: failed to load workflow")
			continue
		}
		if err := e.triggerMgr.ActivateWorkflow(ctx, full); err != nil {
			failed++
			e.logger.Warn().Str("workflow_id", wf.ID).Err(err).Msg("engine: restore: failed to activate triggers")
			continue
		}
		restored++
		caughtUp += e.triggerMgr.CatchUp(ctx, full, time.Now())
	}
	e.logger.Info().Int("restored", restored).Int("failed", failed).Int("catch_up_runs", caughtUp).Msg("engine: restored active workflows")
	loopCtx := e.ctx // the engine's lifetime; ctx when Start was never called
	if loopCtx == nil {
		loopCtx = ctx
	}
	e.startTriggerReconcile(loopCtx)
	return nil
}

// DeleteWorkflow deactivates and deletes a workflow.
func (e *WorkflowEngine) DeleteWorkflow(ctx context.Context, id string) error {
	existing, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return fmt.Errorf("engine: delete workflow: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("engine: delete workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(existing); err != nil {
		return fmt.Errorf("engine: delete workflow: %w", err)
	}

	if existing.IsActive {
		e.triggerMgr.DeactivateWorkflow(id)
	}

	if err := e.store.DeleteWorkflow(ctx, id); err != nil {
		return fmt.Errorf("engine: delete workflow: %w", err)
	}
	if db := e.store.RawDB(); db != nil {
		if err := DeleteWorkflowState(ctx, db, id); err != nil {
			e.logger.Warn().Err(err).Str("workflow_id", id).Msg("engine: delete workflow: state not removed")
		}
	}

	e.logger.Info().Str("workflow_id", id).Msg("engine: workflow deleted")
	return nil
}

// ActivateWorkflow enables a workflow and registers its triggers.
func (e *WorkflowEngine) ActivateWorkflow(ctx context.Context, id string) error {
	wf, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return fmt.Errorf("engine: activate workflow: %w", err)
	}
	if wf == nil {
		return fmt.Errorf("engine: activate workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return fmt.Errorf("engine: activate workflow: %w", err)
	}

	if err := e.store.SetWorkflowActive(ctx, id, true); err != nil {
		return fmt.Errorf("engine: activate workflow: %w", err)
	}
	wf.IsActive = true

	if err := e.triggerMgr.ActivateWorkflow(ctx, wf); err != nil {
		// Registration is per node and best-effort, so the nodes before the
		// failing one are already live. Tear them down before reverting the
		// flag: an inactive workflow whose cron keeps firing is invisible
		// (nothing lists it as active) and DeleteWorkflow only deactivates
		// `if existing.IsActive`, so the trigger would survive deletion.
		e.triggerMgr.DeactivateWorkflow(id)
		// Revert the active flag so the DB stays consistent.
		_ = e.store.SetWorkflowActive(ctx, id, false)
		return fmt.Errorf("engine: activate workflow: register triggers: %w", err)
	}

	e.logger.Info().Str("workflow_id", id).Msg("engine: workflow activated")
	return nil
}

// DeactivateWorkflow disables a workflow and unregisters its triggers.
func (e *WorkflowEngine) DeactivateWorkflow(ctx context.Context, id string) error {
	existing, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return fmt.Errorf("engine: deactivate workflow: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("engine: deactivate workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(existing); err != nil {
		return fmt.Errorf("engine: deactivate workflow: %w", err)
	}

	e.triggerMgr.DeactivateWorkflow(id)

	if err := e.store.SetWorkflowActive(ctx, id, false); err != nil {
		return fmt.Errorf("engine: deactivate workflow: %w", err)
	}

	e.logger.Info().Str("workflow_id", id).Msg("engine: workflow deactivated")
	return nil
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

// GetWorkflow loads a workflow with all nodes and connections.
func (e *WorkflowEngine) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	wf, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("engine: get workflow: %w", err)
	}
	if wf == nil {
		return nil, fmt.Errorf("engine: get workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return nil, fmt.Errorf("engine: get workflow: %w", err)
	}
	return wf, nil
}

// GetExecution loads a workflow execution with all node results.
func (e *WorkflowEngine) GetExecution(ctx context.Context, id string) (*WorkflowExecution, error) {
	exec, err := e.store.GetExecution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("engine: get execution: %w", err)
	}
	if exec == nil {
		return nil, fmt.Errorf("engine: get execution: %w", ErrExecutionNotFound)
	}
	return exec, nil
}
