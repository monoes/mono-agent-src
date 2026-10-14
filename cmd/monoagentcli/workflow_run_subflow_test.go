package main

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// slowNode passes its items on after a pause longer than one wait poll.
type slowNode struct{}

func (slowNode) Type() string { return "test.slow" }
func (slowNode) Execute(ctx context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []workflow.NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// A parent paused only on its sub-workflow finishes within one wait: the
// waiting engine runs the child and resumes the parent, no daemon needed.
func TestWaitForExecutionWaitsThroughSubWorkflows(t *testing.T) {
	db, err := storage.NewDatabase(t.TempDir() + "/run.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	reg := workflow.NewNodeTypeRegistry()
	control.RegisterAll(reg)
	reg.Register("test.slow", func() workflow.NodeExecutor { return slowNode{} })
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	engine := workflow.NewWorkflowEngineWithStore(store, db.DB, nil, reg,
		workflow.EngineConfig{ProfileID: "default", WebhookAddr: "127.0.0.1:0", MaxConcurrent: 1}, zerolog.Nop())
	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop() //nolint:errcheck

	save := func(id, trigger string, next workflow.WorkflowNode) {
		next.ID, next.WorkflowID, next.Name = id+"-1", id, id+"-1"
		wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default", Nodes: []workflow.WorkflowNode{
			{ID: id + "-t", WorkflowID: id, Type: trigger, Name: "Trigger"}, next},
			Connections: []workflow.WorkflowConnection{{SourceNodeID: id + "-t", SourceHandle: "main", TargetNodeID: id + "-1", TargetHandle: "main"}}}
		if err := engine.CreateWorkflow(ctx, wf); err != nil {
			t.Fatal(err)
		}
		if err := store.SetWorkflowActive(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	save("child", workflow.TriggerTypeExecuteWorkflow, workflow.WorkflowNode{Type: "test.slow"})
	save("parent", "trigger.manual", workflow.WorkflowNode{Type: "core.execute_workflow", Config: map[string]interface{}{"workflowId": "child"}})

	id, err := engine.TriggerWorkflow(ctx, "parent", map[string]interface{}{"n": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForExecution(ctx, engine, id, 30*time.Second, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := engine.GetExecutionStatus(ctx, id); st != "SUCCESS" {
		t.Fatalf("wait returned with the parent %s, want SUCCESS", st)
	}
}
