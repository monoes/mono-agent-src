package control

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A WAITING run waits on its sub-workflows while a node that started
// waited-on children has not completed and no human review is pending.
func TestWaitingOnSubWorkflows(t *testing.T) {
	f := newSubflowFixture(t)
	ctx := context.Background()
	db := f.store.RawDB()
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "x"}))
	parent := &workflow.WorkflowExecution{WorkflowID: "parent", ProfileID: "default", Status: "QUEUED", TriggerType: "trigger.manual", PID: 1}
	if err := f.store.CreateExecution(ctx, parent); err != nil {
		t.Fatal(err)
	}
	waiting := func() bool {
		t.Helper()
		ok, err := f.eng.WaitingOnSubWorkflows(ctx, parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}

	if waiting() {
		t.Fatal("a QUEUED run is not waiting")
	}
	if err := f.store.SetExecutionWaiting(ctx, parent.ID, `{"completed_nodes":{"parent-t":true}}`); err != nil {
		t.Fatal(err)
	}
	if waiting() {
		t.Fatal("a pause with no children (an org or timed wait) is not a sub-workflow wait")
	}
	exec(`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait) VALUES (?, 'parent-0', 'c1', -1, 1)`, parent.ID)
	if !waiting() {
		t.Fatal("paused at the node that started the child: want a sub-workflow wait")
	}
	exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES ('h1', ?, 'parent', 'n', 'N', 'pending', 'default')`, parent.ID)
	if waiting() {
		t.Fatal("a pending human review is not a sub-workflow wait")
	}
	exec(`UPDATE hil_pending SET status = 'approved'`)
	if err := f.store.SetExecutionWaiting(ctx, parent.ID, `{"completed_nodes":{"parent-t":true,"parent-0":true}}`); err != nil {
		t.Fatal(err)
	}
	if waiting() {
		t.Fatal("the node that started the child has completed; the run waits on something else")
	}
}
