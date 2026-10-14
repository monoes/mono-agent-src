package workflow

import (
	"context"
	"testing"
	"time"
)

// blockNode runs until the run's context ends.
type blockNode struct{}

func (blockNode) Type() string { return "test.block" }
func (blockNode) Execute(ctx context.Context, _ NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A run its executionTimeout ends is CANCELLED with the timeout message and
// class timeout, and starts no error workflow.
func TestEngineExecutionTimeoutEndsCancelled(t *testing.T) {
	r := newErrorWorkflowRig(t, nil)
	r.eng.registry.Register("test.block", func() NodeExecutor { return blockNode{} })
	r.addWorkflow(t, "wf-err", TriggerNodeTypeError, "test.capture", nil, false)
	r.addWorkflow(t, "wf-main", "trigger.manual", "test.block", map[string]interface{}{
		ErrorWorkflowSetting: "wf-err", ExecutionTimeoutSetting: 0.3,
	}, true)
	execID, err := r.eng.TriggerWorkflow(context.Background(), "wf-main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := waitForTerminalStatus(t, r.store, execID, 10*time.Second); st != "CANCELLED" {
		t.Fatalf("status = %s, want CANCELLED", st)
	}
	ex, _ := r.store.GetExecution(context.Background(), execID)
	if ex.ErrorMessage != "execution timeout after 0.3s" {
		t.Errorf("error_message = %q", ex.ErrorMessage)
	}
	if ex.ErrorClass != string(ClassTimeout) {
		t.Errorf("error_class = %q, want timeout", ex.ErrorClass)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(r.errorRuns(t, "wf-err")); n != 0 {
		t.Fatalf("a timed-out run started %d error workflow runs", n)
	}
}
