package mcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A profile's engine outlives the tool call that first started it: after that
// call's context is cancelled, a later run on the same profile still executes.
func TestEngineOutlivesTheCallThatStartedIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt, err := newRuntime(Options{DBPath: filepath.Join(t.TempDir(), "m.db"), WorkflowsDir: filepath.Join(t.TempDir(), "wf")})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	wf := &workflow.Workflow{
		ProfileID: "default", ID: "wf-twice", Name: "twice", IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}},
	}
	if err := rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithCancel(context.Background())
	engine, err := rt.ensureEngine(callCtx, "default")
	if err != nil {
		t.Fatal(err)
	}
	cancel() // the first call is over

	execID, err := engine.TriggerWorkflow(context.Background(), "wf-twice", nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := waitForExecution(context.Background(), rt, execID, 5*time.Second, false)
	if exec.Status != "SUCCESS" {
		t.Fatalf("run after the starting call ended: status %q", exec.Status)
	}
}
