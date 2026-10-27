package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// waitingParent is a run WAITING only for a sub-workflow it started.
func waitingParent(t *testing.T, rt *runtime) string {
	t.Helper()
	ctx := context.Background()
	if err := rt.store.CreateWorkflow(ctx, &workflow.Workflow{ID: "wf-parent", Name: "Parent", ProfileID: "default"}); err != nil {
		t.Fatal(err)
	}
	exec := &workflow.WorkflowExecution{ID: "exec-parent", WorkflowID: "wf-parent", ProfileID: "default", Status: "QUEUED", TriggerType: "manual"}
	if err := rt.store.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.SetExecutionWaiting(ctx, exec.ID, `{"completed_nodes":{"t":true}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.db.DB.Exec(`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait)
		VALUES ('exec-parent', 'call', 'child-1', -1, 1)`); err != nil {
		t.Fatal(err)
	}
	return exec.ID
}

// By default workflow_run's wait returns at any WAITING, a pause for the
// run's own sub-workflows included, as it always did. With
// wait_through_subworkflows it waits through that pause (this server's
// engine runs the child and resumes the parent) to its timeout, where the run
// still counts as in flight (timed_out). A pending review still returns at
// once.
func TestWorkflowRunWaitThroughSubWorkflowsIsOptIn(t *testing.T) {
	s := newTestServer(t)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := waitingParent(t, rt)

	start := time.Now()
	if got := waitForExecution(ctx, rt, id, 10*time.Second, false); got.Status != "WAITING" || time.Since(start) > 5*time.Second {
		t.Errorf("default: %s after %v, want WAITING at once", got.Status, time.Since(start))
	}

	const timeout = 1500 * time.Millisecond
	start = time.Now()
	got := waitForExecution(ctx, rt, id, timeout, true)
	if waited := time.Since(start); waited < timeout || got.Status != "WAITING" {
		t.Fatalf("opt-in: returned %s after %v, want it to wait %v", got.Status, waited, timeout)
	}
	if inFlight, err := workflow.RecordInFlight(ctx, rt.store, got); err != nil || !inFlight {
		t.Errorf("opt-in at the timeout: in flight %v (%v), want true (timed_out)", inFlight, err)
	}

	if _, err := rt.db.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id)
		VALUES ('h1', ?, 'wf-parent', 'n', 'Review', 'pending', 'default')`, id); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if got := waitForExecution(ctx, rt, id, 10*time.Second, true); got.Status != "WAITING" || time.Since(start) > 5*time.Second {
		t.Errorf("opt-in, a pending review: %s after %v, want WAITING at once", got.Status, time.Since(start))
	}
}

// Through the tool: a parent that calls a slow child returns WAITING at once
// by default (no timed_out), with the opt-in and a short timeout WAITING and
// timed_out, and with the opt-in and time enough the child's answer.
func TestWorkflowRunToolWaitsThroughASubWorkflowWhenAsked(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	var child, parent struct {
		ID string `json:"id"`
	}
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{
		"name": "slow-child", "is_active": true,
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": workflow.TriggerTypeExecuteWorkflow, "name": "Called", "config": map[string]interface{}{}},
			{"id": "c", "node_type": "core.code", "name": "Answer", "config": map[string]interface{}{
				"code": "const until = Date.now() + 2500; while (Date.now() < until) {}\nreturn [{ json: { output: \"child done\" } }]"}},
		},
		"connections": []map[string]interface{}{{"id": "c1", "source_node_id": "t", "source_handle": "main", "target_node_id": "c", "target_handle": "main"}},
	}}, &child)
	activateForTest(t, s, child.ID)
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{
		"name": "caller", "is_active": true,
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Start", "config": map[string]interface{}{}},
			{"id": "x", "node_type": "core.execute_workflow", "name": "Call", "config": map[string]interface{}{"workflowId": child.ID}},
		},
		"connections": []map[string]interface{}{{"id": "c1", "source_node_id": "t", "source_handle": "main", "target_node_id": "x", "target_handle": "main"}},
	}}, &parent)
	activateForTest(t, s, parent.ID)

	type result struct {
		Status   string `json:"status"`
		TimedOut bool   `json:"timed_out"`
		Nodes    []struct {
			NodeID      string          `json:"node_id"`
			OutputItems json.RawMessage `json:"output_items"`
		} `json:"nodes"`
	}
	run := func(args map[string]interface{}) result {
		t.Helper()
		args["id"] = parent.ID
		var r result
		callInto(t, s, "workflow_run", args, &r)
		return r
	}
	if r := run(map[string]interface{}{}); r.Status != "WAITING" || r.TimedOut {
		t.Errorf("default: %+v, want WAITING without timed_out", r)
	}
	if r := run(map[string]interface{}{"wait_through_subworkflows": true, "timeout_seconds": 1}); r.Status != "WAITING" || !r.TimedOut {
		t.Errorf("opt-in, 1s: %+v, want WAITING with timed_out", r)
	}
	r := run(map[string]interface{}{"wait_through_subworkflows": true, "timeout_seconds": 60})
	answered := false
	for _, n := range r.Nodes {
		if n.NodeID == "x" && json.Valid(n.OutputItems) && string(n.OutputItems) != "[]" {
			var items []workflow.Item
			_ = json.Unmarshal(n.OutputItems, &items)
			answered = len(items) == 1 && items[0].JSON["output"] == "child done"
		}
	}
	if r.Status != "SUCCESS" || r.TimedOut || !answered {
		t.Errorf("opt-in, 60s: %+v, want SUCCESS with the child's answer", r)
	}
}
