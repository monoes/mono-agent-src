package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/vault"
)

// subWorkflowWakeWindow bounds how long a finished child keeps trying to wake
// a parent that has started it but not yet persisted WAITING. Past it the
// parent's own pause window (the resume poll) still picks it up.
const subWorkflowWakeWindow = 5 * time.Second

// StartSubWorkflow implements SubWorkflowRunner: it persists a QUEUED child
// execution carrying the items, links it to the parent node, and enqueues it.
// The child runs whether or not its workflow is active.
func (e *WorkflowEngine) StartSubWorkflow(ctx context.Context, req SubWorkflowStart) (string, error) {
	parentData := TriggerDataFrom(ctx)
	depth := subWorkflowDepth(parentData) + 1
	if depth > MaxSubWorkflowDepth {
		return "", fmt.Errorf("sub-workflow call depth exceeds %d (a workflow calling itself without end?)", MaxSubWorkflowDepth)
	}
	profileID := vault.ProfileIDFromContext(ctx)
	if profileID == "" {
		profileID = "default"
	}
	wf, err := e.resolveSubWorkflow(ctx, req.Workflow, profileID)
	if err != nil {
		return "", err
	}

	raw := make([]interface{}, 0, len(req.Items))
	for _, it := range req.Items {
		raw = append(raw, subWorkflowItemData(it))
	}
	data := map[string]interface{}{
		SubWorkflowItemsKey: raw,
		SubWorkflowParentKey: map[string]interface{}{
			"execution_id": req.ParentExecutionID,
			"node_id":      req.ParentNodeID,
			"depth":        depth,
		},
	}
	// The child inherits the parent's workdir confinement and chain trace,
	// so a call cannot step outside what the parent run was allowed.
	for _, k := range []string{"org", "trace"} {
		if v, ok := parentData[k]; ok {
			data[k] = v
		}
	}

	exec := &WorkflowExecution{
		WorkflowID:  wf.ID,
		ProfileID:   profileID,
		Status:      "QUEUED",
		TriggerType: TriggerTypeExecuteWorkflow,
		TriggerData: data,
	}
	dctx, cancel := dbCtx()
	defer cancel()
	if err := e.store.CreateExecution(dctx, exec); err != nil {
		return "", fmt.Errorf("sub-workflow: create execution: %w", err)
	}
	// Linked before it is enqueued, so the child's end always finds its parent.
	if db := e.store.RawDB(); db != nil {
		if _, err := db.ExecContext(dctx,
			`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait) VALUES (?, ?, ?, ?, ?)`,
			req.ParentExecutionID, req.ParentNodeID, exec.ID, req.ItemIndex, req.Wait); err != nil {
			e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "sub-workflow link not recorded")
			return "", fmt.Errorf("sub-workflow: link execution: %w", err)
		}
	}
	if err := e.queue.Enqueue(ExecutionRequest{
		WorkflowID:    wf.ID,
		ExecutionID:   exec.ID,
		TriggerType:   TriggerTypeExecuteWorkflow,
		TriggerNodeID: subWorkflowTriggerNode(wf),
		TriggerData:   data,
	}); err != nil {
		e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("sub-workflow: %w", ErrQueueFull)
	}
	return exec.ID, nil
}

// resolveSubWorkflow finds the workflow ref names: an ID first, then an exact
// name within profileID. A workflow of another profile is never returned.
func (e *WorkflowEngine) resolveSubWorkflow(ctx context.Context, ref, profileID string) (*Workflow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("%w: sub-workflow: no workflow given", ErrInvalidConfig)
	}
	wf, err := e.store.GetWorkflow(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow: load workflow: %w", err)
	}
	if wf != nil {
		if executionProfileID(wf, "default") != profileID {
			return nil, fmt.Errorf("sub-workflow %q: %w", ref, ErrWorkflowProfileMismatch)
		}
		return wf, nil
	}
	all, err := e.store.ListWorkflows(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow: list workflows: %w", err)
	}
	var match *Workflow
	for i := range all {
		if all[i].Name != ref {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("sub-workflow: more than one workflow is named %q; use its ID", ref)
		}
		match = &all[i]
	}
	if match == nil {
		return nil, fmt.Errorf("sub-workflow %q: %w", ref, ErrWorkflowNotFound)
	}
	// ListWorkflows may leave nodes out; load the full record.
	return e.store.GetWorkflow(ctx, match.ID)
}

// subWorkflowTriggerNode picks the child's "When called by another workflow"
// trigger so only its branch fires; "" (every trigger fires) when it has none.
func subWorkflowTriggerNode(wf *Workflow) string {
	for _, n := range wf.Nodes {
		if n.Type == TriggerTypeExecuteWorkflow && !n.Disabled {
			return n.ID
		}
	}
	return ""
}

// SubWorkflowChildren implements SubWorkflowRunner.
func (e *WorkflowEngine) SubWorkflowChildren(ctx context.Context, parentExecutionID, parentNodeID string) ([]SubWorkflowChild, error) {
	db := e.store.RawDB()
	if db == nil {
		return nil, errors.New("sub-workflow: database not available")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT child_execution_id, item_index FROM subworkflow_runs
		 WHERE parent_execution_id = ? AND parent_node_id = ? ORDER BY item_index, rowid`,
		parentExecutionID, parentNodeID)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow: list children: %w", err)
	}
	var out []SubWorkflowChild
	for rows.Next() {
		var c SubWorkflowChild
		if err := rows.Scan(&c.ExecutionID, &c.ItemIndex); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sub-workflow: list children: %w", err)
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sub-workflow: list children: %w", err)
	}
	for i := range out {
		if err := e.fillSubWorkflowChild(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fillSubWorkflowChild loads a child's status and, once it has finished, the
// main items of its last successful node (a trigger's items are its input).
func (e *WorkflowEngine) fillSubWorkflowChild(ctx context.Context, c *SubWorkflowChild) error {
	exec, err := e.store.GetExecution(ctx, c.ExecutionID)
	if err != nil {
		return fmt.Errorf("sub-workflow: load execution %s: %w", c.ExecutionID, err)
	}
	if exec == nil {
		c.Status, c.Error = "FAILED", "execution record not found"
		return nil
	}
	c.Status, c.Error = exec.Status, exec.ErrorMessage
	if !c.Done() || c.Failed() {
		return nil
	}
	for i := len(exec.Nodes) - 1; i >= 0; i-- {
		n := exec.Nodes[i]
		if n.Status != "SUCCESS" {
			continue
		}
		c.Items = n.OutputItems
		if len(c.Items) == 0 && e.isTriggerNode(ctx, exec.WorkflowID, n.NodeID) {
			c.Items = n.InputItems
		}
		break
	}
	if c.Items == nil {
		c.Items = []Item{}
	}
	return nil
}

// isTriggerNode reports whether nodeID is a trigger of workflowID. A trigger's
// pass-through record stores its items as input only.
func (e *WorkflowEngine) isTriggerNode(ctx context.Context, workflowID, nodeID string) bool {
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return false
	}
	for _, n := range wf.Nodes {
		if n.ID == nodeID {
			return strings.HasPrefix(n.Type, "trigger.")
		}
	}
	return false
}

// onExecutionFinished wakes a waiting parent when executionID was the last of
// its running sub-workflow children. Called after a terminal status is
// persisted; returns at once for executions no parent waits on.
func (e *WorkflowEngine) onExecutionFinished(executionID string) {
	db := e.store.RawDB()
	if db == nil {
		return
	}
	dctx, cancel := dbCtx()
	defer cancel()
	var parentID, nodeID string
	err := db.QueryRowContext(dctx,
		`SELECT parent_execution_id, parent_node_id FROM subworkflow_runs WHERE child_execution_id = ? AND wait = 1`,
		executionID).Scan(&parentID, &nodeID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table") {
			e.logger.Warn().Err(err).Str("execution_id", executionID).Msg("engine: sub-workflow parent lookup")
		}
		return
	}
	go e.wakeSubWorkflowParent(parentID, nodeID)
}

// wakeSubWorkflowParent resumes parentID once every child its node started
// has finished. A parent still RUNNING is between starting its children and
// persisting WAITING; it is retried briefly rather than missed.
func (e *WorkflowEngine) wakeSubWorkflowParent(parentID, nodeID string) {
	dctx, cancel := dbCtx()
	children, err := e.SubWorkflowChildren(dctx, parentID, nodeID)
	cancel()
	if err != nil {
		e.logger.Warn().Err(err).Str("execution_id", parentID).Msg("engine: sub-workflow wake")
		return
	}
	for _, c := range children {
		if !c.Done() {
			return // the last child to finish wakes the parent
		}
	}
	deadline := time.Now().Add(subWorkflowWakeWindow)
	for {
		sctx, scancel := dbCtx()
		status, err := e.store.GetExecutionStatus(sctx, parentID)
		scancel()
		if err != nil {
			return
		}
		switch status {
		case "WAITING":
			if err := e.ResumeExecution(parentID); err != nil {
				e.logger.Warn().Err(err).Str("execution_id", parentID).Msg("engine: sub-workflow parent resume")
			}
			return
		case "RUNNING", "QUEUED":
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		default:
			return
		}
	}
}

// cancelSubWorkflowChildren cancels the unfinished children parentID started
// through cancelExecution, which in turn cancels theirs. A child running in
// another process is not reachable from here; `workflow cancel` reaches it
// (cancelExecution in cmd/monoagentcli signals its process).
func (e *WorkflowEngine) cancelSubWorkflowChildren(parentID string, cause error) {
	db := e.store.RawDB()
	if db == nil {
		return
	}
	dctx, cancel := dbCtx()
	ids, err := UnfinishedSubWorkflowChildren(dctx, db, parentID)
	cancel()
	if err != nil {
		e.logger.Warn().Err(err).Str("execution_id", parentID).Msg("engine: sub-workflow cancel cascade")
		return
	}
	for _, id := range ids {
		e.logger.Info().Str("execution_id", id).Str("parent_execution_id", parentID).Msg("engine: cancelling sub-workflow child with its parent")
		e.cancelExecution(id, cause)
	}
}

// WaitingOnSubWorkflows reports whether executionID is WAITING for its
// sub-workflow children: a node that started children it waits on has not
// completed, and no human review is pending. Such a run needs only a live
// engine (this one runs the children it started, and resumes the parent).
func (e *WorkflowEngine) WaitingOnSubWorkflows(ctx context.Context, executionID string) (bool, error) {
	db := e.store.RawDB()
	if db == nil {
		return false, nil
	}
	exec, err := e.store.GetExecution(ctx, executionID)
	if err != nil {
		return false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	if exec == nil || exec.Status != "WAITING" {
		return false, nil
	}
	var reviews int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hil_pending WHERE execution_id = ? AND status = 'pending'`, executionID).Scan(&reviews); err != nil {
		return false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	if reviews > 0 {
		return false, nil
	}
	var state executionResumeState
	if exec.ResumeState != "" {
		if err := json.Unmarshal([]byte(exec.ResumeState), &state); err != nil {
			return false, nil
		}
	}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT parent_node_id FROM subworkflow_runs WHERE parent_execution_id = ? AND wait = 1`, executionID)
	if err != nil {
		return false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return false, fmt.Errorf("sub-workflow wait: %w", err)
		}
		if !state.CompletedNodes[nodeID] {
			return true, nil
		}
	}
	return false, rows.Err()
}
