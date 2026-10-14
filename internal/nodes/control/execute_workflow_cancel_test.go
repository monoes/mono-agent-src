package control

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// blockNode runs until its execution is cancelled.
type blockNode struct{}

func (blockNode) Type() string { return "test.block" }
func (blockNode) Execute(ctx context.Context, _ workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// childOf waits for parentID's first child to reach one of statuses and
// returns its ID.
func (f *subflowFixture) childOf(t *testing.T, parentID string, statuses ...string) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var id string
		if err := f.store.RawDB().QueryRow(`SELECT child_execution_id FROM subworkflow_runs WHERE parent_execution_id = ?`, parentID).Scan(&id); err != nil {
			continue
		}
		st, _ := f.store.GetExecutionStatus(context.Background(), id)
		for _, s := range statuses {
			if st == s {
				return id
			}
		}
	}
	t.Fatalf("no child of %s reached %v", parentID, statuses)
	return ""
}

func (f *subflowFixture) waitStatus(t *testing.T, id, want string) {
	t.Helper()
	if st := f.waitTerminal(t, id); st != want {
		t.Fatalf("execution %s = %s, want %s", id, st, want)
	}
}

func TestExecuteWorkflow_CancelWaitingParentCancelsChildrenRecursively(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "grand", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.block"})
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, execNode(map[string]interface{}{"workflowId": "grand"}))
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "child"}))

	parentID, err := f.eng.TriggerWorkflow(context.Background(), "parent", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	childID := f.childOf(t, parentID, "WAITING")
	grandID := f.childOf(t, childID, "RUNNING")

	f.eng.CancelExecution(parentID)

	f.waitStatus(t, parentID, "CANCELLED")
	f.waitStatus(t, childID, "CANCELLED")
	f.waitStatus(t, grandID, "CANCELLED")
}

func TestExecuteWorkflow_CancelRunningParentCancelsQueuedChild(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.mark"})
	// The parent keeps the only slot, so its fire-and-forget child stays QUEUED.
	f.chain(t, "parent", "trigger.manual", true,
		execNode(map[string]interface{}{"workflowId": "child", "waitForSubWorkflow": false}),
		workflow.WorkflowNode{Type: "test.block"})

	parentID, err := f.eng.TriggerWorkflow(context.Background(), "parent", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	childID := f.childOf(t, parentID, "QUEUED")

	f.eng.CancelExecution(parentID)

	f.waitStatus(t, parentID, "CANCELLED")
	f.waitStatus(t, childID, "CANCELLED")
}

func TestExecuteWorkflow_CancelFinishedParentLeavesChildren(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.block"})
	f.chain(t, "parent", "trigger.manual", true,
		execNode(map[string]interface{}{"workflowId": "child", "waitForSubWorkflow": false}))

	parentID, err := f.eng.TriggerWorkflow(context.Background(), "parent", nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	f.waitStatus(t, parentID, "SUCCESS")
	childID := f.childOf(t, parentID, "RUNNING")

	f.eng.CancelExecution(parentID) // a finished run: nothing to cancel

	time.Sleep(200 * time.Millisecond)
	if st, _ := f.store.GetExecutionStatus(context.Background(), childID); st != "RUNNING" {
		t.Fatalf("detached child = %s, want RUNNING", st)
	}
	f.eng.CancelExecution(childID)
	f.waitStatus(t, childID, "CANCELLED")
}

// A child cancelled before it ran wakes its waiting parent at once, not on
// the parent's 30s resume poll.
func TestExecuteWorkflow_QueuedChildCancelWakesParent(t *testing.T) {
	f := newSubflowFixture(t)
	ctx := context.Background()
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.mark"})
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "child"}))

	// A parent paused on a child that never left the queue.
	parent := &workflow.WorkflowExecution{WorkflowID: "parent", ProfileID: "default", Status: "QUEUED", TriggerType: "trigger.manual", PID: 1}
	child := &workflow.WorkflowExecution{WorkflowID: "child", ProfileID: "default", Status: "QUEUED", TriggerType: workflow.TriggerTypeExecuteWorkflow, PID: 1}
	for _, x := range []*workflow.WorkflowExecution{parent, child} {
		if err := f.store.CreateExecution(ctx, x); err != nil {
			t.Fatalf("create execution: %v", err)
		}
	}
	if err := f.store.SetExecutionWaiting(ctx, parent.ID, ""); err != nil {
		t.Fatalf("set waiting: %v", err)
	}
	later := time.Now().Add(time.Hour)
	if err := f.store.SetExecutionResumeAfter(ctx, parent.ID, &later); err != nil {
		t.Fatalf("set resume_after: %v", err)
	}
	if _, err := f.store.RawDB().Exec(`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait) VALUES (?, 'parent-0', ?, -1, 1)`,
		parent.ID, child.ID); err != nil {
		t.Fatalf("link: %v", err)
	}

	f.eng.CancelExecution(child.ID)

	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		st, _ := f.store.GetExecutionStatus(ctx, parent.ID)
		if st != "WAITING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parent still WAITING; the cancelled child did not wake it")
		}
	}
}
