package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// By default the run route's wait returns at any WAITING, a pause for the
// run's own sub-workflows included, as it always did. With
// ?wait_through_subworkflows=true it waits through that pause (the engine
// runs the child and resumes the parent) to its timeout, where the run still
// counts as in flight (timed_out). A pending review still returns at once.
func TestRunRouteWaitThroughSubWorkflowsIsOptIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := newTestServer(t, true).rt
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

	start := time.Now()
	if got := waitForExecution(ctx, rt, exec.ID, 10*time.Second, false); got.Status != "WAITING" || time.Since(start) > 5*time.Second {
		t.Errorf("default: %s after %v, want WAITING at once", got.Status, time.Since(start))
	}

	const timeout = 1500 * time.Millisecond
	start = time.Now()
	got := waitForExecution(ctx, rt, exec.ID, timeout, true)
	if waited := time.Since(start); waited < timeout || got.Status != "WAITING" {
		t.Fatalf("opt-in: returned %s after %v, want it to wait %v", got.Status, waited, timeout)
	}
	if inFlight, err := workflow.RecordInFlight(ctx, rt.store, got); err != nil || !inFlight {
		t.Errorf("opt-in at the timeout: in flight %v (%v), want true (timed_out)", inFlight, err)
	}

	if _, err := rt.db.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id)
		VALUES ('h1', 'exec-parent', 'wf-parent', 'n', 'Review', 'pending', 'default')`); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if got := waitForExecution(ctx, rt, exec.ID, 10*time.Second, true); got.Status != "WAITING" || time.Since(start) > 5*time.Second {
		t.Errorf("opt-in, a pending review: %s after %v, want WAITING at once", got.Status, time.Since(start))
	}
}

// Through the route: a parent that calls a slow child returns WAITING at
// once by default (no timed_out), with the opt-in and a short timeout WAITING
// and timed_out, and with the opt-in and time enough SUCCESS. A value that is
// not a boolean is refused.
func TestRunRouteWaitsThroughASubWorkflowWhenAsked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	ctx := context.Background()
	for _, wf := range []*workflow.Workflow{
		{ProfileID: "default", ID: "wf-child", Name: "child", IsActive: true,
			Nodes: []workflow.WorkflowNode{
				{ID: "t", Type: workflow.TriggerTypeExecuteWorkflow, Name: "Called"},
				{ID: "c", Type: "core.code", Name: "Answer", Config: map[string]interface{}{
					"code": "const until = Date.now() + 2500; while (Date.now() < until) {}\nreturn [{ json: { output: \"child done\" } }]"}},
			},
			Connections: []workflow.WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"}}},
		{ProfileID: "default", ID: "wf-caller", Name: "caller", IsActive: true,
			Nodes: []workflow.WorkflowNode{
				{ID: "t", Type: "trigger.manual", Name: "Start"},
				{ID: "x", Type: "core.execute_workflow", Name: "Call", Config: map[string]interface{}{"workflowId": "wf-child"}},
			},
			Connections: []workflow.WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "x", TargetHandle: "main"}}},
	} {
		if err := s.rt.store.CreateWorkflow(workflow.WithApproval(ctx), wf); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		Status   string `json:"status"`
		TimedOut bool   `json:"timed_out"`
	}
	run := func(query, body string) (int, result) {
		t.Helper()
		rw := doReq(t, s, "POST", "/profiles/default/workflows/wf-caller/run"+query, tok, []byte(body))
		var r result
		_ = json.Unmarshal(rw.Body.Bytes(), &r)
		return rw.Code, r
	}
	if code, r := run("", `{}`); code != http.StatusOK || r.Status != "WAITING" || r.TimedOut {
		t.Errorf("default: %d %+v, want WAITING without timed_out", code, r)
	}
	if code, r := run("?wait_through_subworkflows=true", `{"timeout_seconds":1}`); code != http.StatusOK || r.Status != "WAITING" || !r.TimedOut {
		t.Errorf("opt-in, 1s: %d %+v, want WAITING with timed_out", code, r)
	}
	if code, r := run("?wait_through_subworkflows=true", `{"timeout_seconds":60}`); code != http.StatusOK || r.Status != "SUCCESS" || r.TimedOut {
		t.Errorf("opt-in, 60s: %d %+v, want SUCCESS", code, r)
	}
	if code, _ := run("?wait_through_subworkflows=maybe", `{}`); code != http.StatusBadRequest {
		t.Errorf("a value that is not a boolean: %d, want 400", code)
	}
}
