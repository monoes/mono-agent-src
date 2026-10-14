package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// markNode stamps every item with the size of the batch it ran in.
type markNode struct{}

func (markNode) Type() string { return "test.mark" }
func (markNode) Execute(_ context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	out := make([]workflow.Item, 0, len(in.Items))
	for _, it := range in.Items {
		m := copyMap(it.JSON)
		m["batch"] = len(in.Items)
		out = append(out, workflow.NewItem(m))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

type failNode struct{}

func (failNode) Type() string { return "test.fail" }
func (failNode) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	return nil, errors.New("child exploded")
}

type subflowFixture struct {
	eng   *workflow.WorkflowEngine
	store *workflow.SQLiteWorkflowStore
}

// newSubflowFixture starts a real engine over a migrated database with ONE
// concurrency slot: a parent that held its slot while waiting would leave
// its children no slot to run in, so every test here would time out.
func newSubflowFixture(t *testing.T) *subflowFixture {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "subflow.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	reg := workflow.NewNodeTypeRegistry()
	RegisterAll(reg)
	reg.Register("test.mark", func() workflow.NodeExecutor { return markNode{} })
	reg.Register("test.fail", func() workflow.NodeExecutor { return failNode{} })
	reg.Register("test.block", func() workflow.NodeExecutor { return blockNode{} })
	reg.Register("test.makebin", func() workflow.NodeExecutor { return makeBinNode{} })
	reg.Register("test.readbin", func() workflow.NodeExecutor { return readBinNode{} })
	eng := workflow.NewWorkflowEngineWithStore(store, db.DB, nil, reg,
		workflow.EngineConfig{ProfileID: "default", WebhookAddr: "127.0.0.1:0", MaxConcurrent: 1}, zerolog.Nop())
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop() })
	return &subflowFixture{eng: eng, store: store}
}

// chain saves workflow id as trigger → node[0] → node[1] … and returns id.
func (f *subflowFixture) chain(t *testing.T, id, triggerType string, active bool, nodes ...workflow.WorkflowNode) string {
	t.Helper()
	ctx := context.Background()
	wf := &workflow.Workflow{ID: id, Name: id + "-name", ProfileID: "default",
		Nodes: []workflow.WorkflowNode{{ID: id + "-t", WorkflowID: id, Type: triggerType, Name: "Trigger"}}}
	prev := id + "-t"
	for i, n := range nodes {
		n.ID, n.WorkflowID = fmt.Sprintf("%s-%d", id, i), id
		if n.Name == "" {
			n.Name = n.ID
		}
		wf.Nodes = append(wf.Nodes, n)
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{SourceNodeID: prev, SourceHandle: "main", TargetNodeID: n.ID, TargetHandle: "main"})
		prev = n.ID
	}
	if err := f.eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	if active {
		if err := f.store.SetWorkflowActive(ctx, id, true); err != nil {
			t.Fatalf("activate: %v", err)
		}
	}
	return id
}

func execNode(cfg map[string]interface{}) workflow.WorkflowNode {
	return workflow.WorkflowNode{Type: "core.execute_workflow", Config: cfg}
}

// runParent triggers parent with three items and waits for its end.
func (f *subflowFixture) runParent(t *testing.T, parent string) (*workflow.WorkflowExecution, time.Duration) {
	t.Helper()
	ctx := context.Background()
	items := []interface{}{map[string]interface{}{"n": 0.0}, map[string]interface{}{"n": 1.0}, map[string]interface{}{"n": 2.0}}
	start := time.Now()
	id, err := f.eng.TriggerWorkflow(ctx, parent, map[string]interface{}{workflow.SubWorkflowItemsKey: items})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	f.waitTerminal(t, id)
	exec, err := f.store.GetExecution(ctx, id)
	if err != nil || exec == nil {
		t.Fatalf("load execution: %v", err)
	}
	return exec, time.Since(start)
}

func (f *subflowFixture) waitTerminal(t *testing.T, id string) string {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		st, err := f.store.GetExecutionStatus(context.Background(), id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		switch st {
		case "QUEUED", "RUNNING", "WAITING":
			if time.Now().After(deadline) {
				t.Fatalf("execution %s still %s", id, st)
			}
		default:
			return st
		}
	}
}

// nodeRecord returns the last record of nodeID in exec.
func nodeRecord(t *testing.T, exec *workflow.WorkflowExecution, nodeID string) workflow.WorkflowExecutionNode {
	t.Helper()
	for i := len(exec.Nodes) - 1; i >= 0; i-- {
		if exec.Nodes[i].NodeID == nodeID {
			return exec.Nodes[i]
		}
	}
	t.Fatalf("no record for node %s", nodeID)
	return workflow.WorkflowExecutionNode{}
}

func childCount(t *testing.T, f *subflowFixture, parentID string) int {
	t.Helper()
	var n int
	if err := f.store.RawDB().QueryRow(`SELECT COUNT(*) FROM subworkflow_runs WHERE parent_execution_id = ?`, parentID).Scan(&n); err != nil {
		t.Fatalf("count children: %v", err)
	}
	return n
}

func TestExecuteWorkflow_OnceRunsOneChildWithAllItems(t *testing.T) {
	f := newSubflowFixture(t)
	// The child is inactive and called by name.
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.mark"})
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "child-name", "mode": "once"}))

	exec, took := f.runParent(t, "parent")
	if exec.Status != "SUCCESS" {
		t.Fatalf("parent status = %s (%s), want SUCCESS", exec.Status, exec.ErrorMessage)
	}
	// Woken by the child's end, not by the 30s resume poll.
	if took > 10*time.Second {
		t.Errorf("parent took %s; the child's end should wake it", took)
	}
	if n := childCount(t, f, exec.ID); n != 1 {
		t.Fatalf("children = %d, want 1", n)
	}
	out := nodeRecord(t, exec, "parent-0").OutputItems
	if len(out) != 3 {
		t.Fatalf("output items = %d, want 3", len(out))
	}
	for i, it := range out {
		if it.JSON["batch"] != 3.0 || it.JSON["n"] != float64(i) {
			t.Errorf("item %d = %v, want n=%d batch=3", i, it.JSON, i)
		}
	}
	paused := false
	for _, n := range exec.Nodes {
		paused = paused || (n.NodeID == "parent-0" && n.Status == "PAUSED")
	}
	if !paused {
		t.Error("the parent node never paused; it must wait without holding its slot")
	}
}

func TestExecuteWorkflow_EachRunsOneChildPerItem(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.mark"})
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "child", "mode": "each"}))

	exec, _ := f.runParent(t, "parent")
	if exec.Status != "SUCCESS" {
		t.Fatalf("parent status = %s (%s), want SUCCESS", exec.Status, exec.ErrorMessage)
	}
	if n := childCount(t, f, exec.ID); n != 3 {
		t.Fatalf("children = %d, want 3", n)
	}
	out := nodeRecord(t, exec, "parent-0").OutputItems
	if len(out) != 3 {
		t.Fatalf("output items = %d, want 3", len(out))
	}
	for i, it := range out {
		if it.JSON["batch"] != 1.0 || it.JSON["n"] != float64(i) {
			t.Errorf("item %d = %v, want n=%d batch=1", i, it.JSON, i)
		}
	}
}

func TestExecuteWorkflow_ChildFailureFailsNode(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.fail"})
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "child"}))

	exec, _ := f.runParent(t, "parent")
	if exec.Status != "FAILED" {
		t.Fatalf("parent status = %s, want FAILED", exec.Status)
	}
	var childID string
	_ = f.store.RawDB().QueryRow(`SELECT child_execution_id FROM subworkflow_runs WHERE parent_execution_id = ?`, exec.ID).Scan(&childID)
	if !strings.Contains(exec.ErrorMessage, "child exploded") || childID == "" || !strings.Contains(exec.ErrorMessage, childID) {
		t.Errorf("error = %q, want the child's message and execution id %q", exec.ErrorMessage, childID)
	}
}

func TestExecuteWorkflow_ChildFailureRespectsOnError(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.fail"})
	f.chain(t, "parent", "trigger.manual", true,
		execNode(map[string]interface{}{"workflowId": "child", "on_error": "continue"}), workflow.WorkflowNode{Type: "test.mark"})

	exec, _ := f.runParent(t, "parent")
	if exec.Status != "SUCCESS_WITH_ERRORS" {
		t.Fatalf("parent status = %s (%s), want SUCCESS_WITH_ERRORS", exec.Status, exec.ErrorMessage)
	}
	if got := len(nodeRecord(t, exec, "parent-1").OutputItems); got != 3 {
		t.Errorf("downstream items = %d, want the 3 passed-through inputs", got)
	}
}

func TestExecuteWorkflow_NoWaitEmitsInputsWithChildIDs(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.mark"})
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "child", "mode": "each", "waitForSubWorkflow": false}))

	exec, _ := f.runParent(t, "parent")
	if exec.Status != "SUCCESS" {
		t.Fatalf("parent status = %s (%s), want SUCCESS", exec.Status, exec.ErrorMessage)
	}
	rec := nodeRecord(t, exec, "parent-0")
	if rec.Status != "SUCCESS" {
		t.Errorf("node status = %s; with no wait it must not pause", rec.Status)
	}
	seen := map[string]bool{}
	for i, it := range rec.OutputItems {
		id, _ := it.JSON["subworkflow_execution_id"].(string)
		if id == "" || it.JSON["n"] != float64(i) || it.JSON["batch"] != nil {
			t.Fatalf("item %d = %v, want the input item plus its child id", i, it.JSON)
		}
		seen[id] = true
		if st := f.waitTerminal(t, id); st != "SUCCESS" {
			t.Errorf("child %s = %s, want SUCCESS", id, st)
		}
	}
	if len(seen) != 3 {
		t.Errorf("distinct child ids = %d, want 3", len(seen))
	}
}

func TestExecuteWorkflow_SelfCallStopsAtDepthLimit(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "loop", workflow.TriggerTypeExecuteWorkflow, true, execNode(map[string]interface{}{"workflowId": "loop"}))

	exec, _ := f.runParent(t, "loop")
	if exec.Status != "FAILED" || !strings.Contains(exec.ErrorMessage, "depth exceeds") {
		t.Fatalf("status = %s (%s), want FAILED at the depth limit", exec.Status, exec.ErrorMessage)
	}
	var runs int
	_ = f.store.RawDB().QueryRow(`SELECT COUNT(*) FROM workflow_executions WHERE workflow_id = 'loop'`).Scan(&runs)
	if runs != workflow.MaxSubWorkflowDepth+1 {
		t.Errorf("executions = %d, want %d", runs, workflow.MaxSubWorkflowDepth+1)
	}
}

func TestExecuteWorkflow_UnknownWorkflowFails(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "parent", "trigger.manual", true, execNode(map[string]interface{}{"workflowId": "nope"}))
	exec, _ := f.runParent(t, "parent")
	if exec.Status != "FAILED" || !strings.Contains(exec.ErrorMessage, "not found") {
		t.Fatalf("status = %s (%s), want FAILED not found", exec.Status, exec.ErrorMessage)
	}
}
