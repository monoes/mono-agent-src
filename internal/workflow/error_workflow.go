package workflow

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
)

// TriggerNodeTypeError is the Error Trigger: a workflow that has one can be
// another workflow's error workflow (Settings["error_workflow"], or the
// workflow.error_workflow config default). When that workflow finishes
// FAILED, the error workflow runs from this node with errorTriggerPayload as
// its item. Like trigger.manual it needs no activation.
const TriggerNodeTypeError = "trigger.error"

// recordFinishedClass records the error class of an execution about to be
// written with its terminal status. It runs before that write, so whoever
// sees the terminal status also sees its class: written after it, a reader
// polling the status (`workflow run`, a test) could catch the row CANCELLED
// with the interrupted node's class (or none) instead of timeout.
func (e *WorkflowEngine) recordFinishedClass(executionID, status, errMsg string) {
	ctx := context.WithoutCancel(context.Background())
	db := e.store.RawDB()
	switch status {
	case StatusRejected:
		setExecutionErrorClass(ctx, db, executionID, ClassRejected, true)
	case "CANCELLED":
		// A run its executionTimeout ended is a timeout, whatever class the
		// node interrupted by it recorded.
		if ClassifyMessage(errMsg) == ClassTimeout {
			setExecutionErrorClass(ctx, db, executionID, ClassTimeout, true)
		} else {
			setExecutionErrorClass(ctx, db, executionID, ClassCancelled, false)
		}
	case "FAILED":
		setExecutionErrorClass(ctx, db, executionID, ClassifyMessage(errMsg), false)
	}
}

// afterExecutionFinished runs once an execution's terminal status is
// written: for a FAILED run it starts the workflow's error workflow.
// Cancelled, rejected and successful runs start nothing.
func (e *WorkflowEngine) afterExecutionFinished(log zerolog.Logger, executionID, status, errMsg string) {
	ctx := context.WithoutCancel(context.Background())
	db := e.store.RawDB()
	defer notifyExecutionFinished(db, executionID, status)
	if status != "FAILED" {
		return
	}
	if id, err := e.fireErrorWorkflow(ctx, executionID, errMsg); err != nil {
		log.Warn().Err(err).Str("execution_id", executionID).Msg("engine: error workflow not started")
	} else if id != "" {
		log.Info().Str("execution_id", executionID).Str("error_execution_id", id).Msg("engine: error workflow started")
	}
}

// fireErrorWorkflow starts the error workflow of the failed execution
// executionID, returning the new execution's id ("" when there is no error
// workflow to run). A run that was itself started by an error trigger never
// starts another, so an error workflow that fails cannot loop.
func (e *WorkflowEngine) fireErrorWorkflow(ctx context.Context, executionID, errMsg string) (string, error) {
	failed, err := e.store.GetExecution(ctx, executionID)
	if err != nil || failed == nil {
		return "", err
	}
	if failed.TriggerType == TriggerNodeTypeError {
		return "", nil
	}
	wf, err := e.store.GetWorkflow(ctx, failed.WorkflowID)
	if err != nil || wf == nil {
		return "", err
	}
	// A definition the operator never approved (an agent's or the chat's)
	// names no error workflow of its own: it would start one as the
	// operator's run. An approval check that fails counts as unapproved.
	approved, _ := operatorApproved(ctx, e.store, wf)
	targetID := errorWorkflowID(ctx, e.store.RawDB(), wf, approved)
	if targetID == "" {
		return "", nil
	}
	target, err := e.store.GetWorkflow(ctx, targetID)
	if err != nil {
		return "", fmt.Errorf("load error workflow %s: %w", targetID, err)
	}
	if target == nil {
		return "", fmt.Errorf("error workflow %s: %w", targetID, ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(target); err != nil {
		return "", fmt.Errorf("error workflow %s: %w", targetID, err)
	}
	triggerNodeID := ""
	for _, n := range target.Nodes {
		if n.Type == TriggerNodeTypeError && !n.Disabled {
			triggerNodeID = n.ID
			break
		}
	}
	if triggerNodeID == "" {
		return "", fmt.Errorf("error workflow %s (%s) has no enabled %s node", target.Name, targetID, TriggerNodeTypeError)
	}

	exec := &WorkflowExecution{
		WorkflowID:  target.ID,
		ProfileID:   executionProfileID(target),
		Status:      "QUEUED",
		TriggerType: TriggerNodeTypeError,
		TriggerData: errorTriggerPayload(failed, wf, errMsg),
	}
	if err := e.store.CreateExecution(ctx, exec); err != nil {
		return "", fmt.Errorf("create error workflow execution: %w", err)
	}
	req := ExecutionRequest{
		WorkflowID:    target.ID,
		ExecutionID:   exec.ID,
		TriggerType:   TriggerNodeTypeError,
		TriggerNodeID: triggerNodeID,
		TriggerData:   exec.TriggerData,
	}
	if err := e.queue.Enqueue(req); err != nil {
		e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("enqueue error workflow: %w", err)
	}
	return exec.ID, nil
}

// errorTriggerPayload is the item an Error Trigger emits. Its shape follows
// the error-trigger payload other workflow tools use ({execution, workflow}),
// so expressions such as {{ $json.execution.error.message }} carry over, and
// adds the error class and fix hint of the failure.
func errorTriggerPayload(failed *WorkflowExecution, wf *Workflow, errMsg string) map[string]interface{} {
	errObj := map[string]interface{}{"message": errMsg}
	lastNode := ""
	for _, n := range failed.Nodes {
		if n.Status == "SKIPPED" {
			continue
		}
		lastNode = n.NodeName
		if n.Status == "FAILED" {
			node := map[string]interface{}{"id": n.NodeID, "name": n.NodeName}
			for _, wn := range wf.Nodes {
				if wn.ID == n.NodeID {
					node["type"] = wn.Type
				}
			}
			errObj["node"] = node
		}
	}
	class := ErrorClass(failed.ErrorClass)
	if class == "" {
		class = ClassifyMessage(errMsg)
	}
	return map[string]interface{}{
		"execution": map[string]interface{}{
			"id":               failed.ID,
			"error":            errObj,
			"lastNodeExecuted": lastNode,
			"mode":             executionMode(failed.TriggerType),
			"trigger_type":     failed.TriggerType,
			"error_class":      string(class),
			"error_hint":       ErrorHint(class),
		},
		"workflow": map[string]interface{}{
			"id":   wf.ID,
			"name": wf.Name,
		},
	}
}

// executionMode maps a trigger type to the coarse run mode an error
// workflow sees: manual, webhook or trigger.
func executionMode(triggerType string) string {
	switch triggerType {
	case "trigger.manual", "":
		return "manual"
	case TriggerNodeTypeWebhook:
		return "webhook"
	case TriggerTypeEvaluation:
		return "evaluation"
	}
	return "trigger"
}
