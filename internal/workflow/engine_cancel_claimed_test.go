package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// cancelOnLoadStore cancels the run that is loading its workflow, the way a
// cancel lands on a run a worker has just claimed (QUEUED → RUNNING) and not
// yet started.
type cancelOnLoadStore struct {
	*SQLiteWorkflowStore
	eng *WorkflowEngine
}

func (s *cancelOnLoadStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	var running string
	_ = s.RawDB().QueryRowContext(ctx, `SELECT id FROM workflow_executions WHERE status = 'RUNNING'`).Scan(&running)
	if running == "" || s.eng == nil {
		return s.SQLiteWorkflowStore.GetWorkflow(ctx, id)
	}
	s.eng.CancelExecution(running)
	<-ctx.Done()
	return nil, ctx.Err()
}

// A run cancelled between its claim and its start ends CANCELLED, not
// FAILED with "load workflow: context canceled".
func TestEngine_CancelAfterClaimEndsCancelled(t *testing.T) {
	store := &cancelOnLoadStore{SQLiteWorkflowStore: newFullEngineStore(t)}
	reg := NewNodeTypeRegistry()
	reg.Register("test.block", func() NodeExecutor { return blockingCountNode{runs: new(int32), d: time.Millisecond} })
	eng := NewWorkflowEngineWithStore(store, store.RawDB(), nil, reg, EngineConfig{ProfileID: "default"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wfID := createActiveManualWorkflow(t, ctx, eng, "test.block")
	store.eng = eng
	eng.queue.Start(ctx)

	id, err := eng.TriggerWorkflow(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		exec, err := store.GetExecution(ctx, id)
		if err != nil || exec == nil {
			t.Fatalf("load execution: %v", err)
		}
		if isTerminalStatus(exec.Status) {
			if exec.Status != "CANCELLED" {
				t.Fatalf("status = %s (%q), want CANCELLED", exec.Status, exec.ErrorMessage)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution still %s", exec.Status)
		}
	}
}

// r01-r01-9: an execution waiting for a concurrency slot that is cancelled
// ends CANCELLED (the cancel path writes it) and leaves no goroutine behind.
func TestEngine_CancelWhileWaitingForSlotEndsCancelled(t *testing.T) {
	store := newFullEngineStore(t)
	reg := NewNodeTypeRegistry()
	reg.Register("test.block", func() NodeExecutor { return blockingCountNode{runs: new(int32), d: time.Millisecond} })
	eng := NewWorkflowEngineWithStore(store, store.RawDB(), nil, reg, EngineConfig{ProfileID: "default", MaxConcurrent: 1}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wfID := createActiveManualWorkflow(t, ctx, eng, "test.block")
	eng.queue.gate.tokens <- struct{}{} // every slot is taken
	eng.queue.Start(ctx)

	id, err := eng.TriggerWorkflow(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, ok := eng.queue.cancelFuncs.Load(id); ok {
			break // the run is waiting in the gate
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never reached the gate")
		}
	}
	eng.CancelExecution(id)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, ok := eng.queue.cancelFuncs.Load(id); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancelled run is still waiting")
		}
	}
	exec, err := store.GetExecution(ctx, id)
	if err != nil || exec == nil {
		t.Fatalf("load execution: %v", err)
	}
	if exec.Status != "CANCELLED" {
		t.Fatalf("status = %s, want CANCELLED", exec.Status)
	}
}
