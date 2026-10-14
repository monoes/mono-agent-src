package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// sleepNode waits d (or until the run's context ends) and passes its items on.
type sleepNode struct{ d time.Duration }

func (sleepNode) Type() string { return "test.sleep" }
func (s sleepNode) Execute(ctx context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	select {
	case <-time.After(s.d):
		return []workflow.NodeOutput{{Handle: "main", Items: in.Items}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func timeoutWorkflow(timeout interface{}) *workflow.Workflow {
	wf := &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "T"},
		{ID: "a", Type: "test.sleep", Name: "A"},
		{ID: "b", Type: "test.sleep", Name: "B"},
	}, Connections: []workflow.WorkflowConnection{conn("t", "main", "a"), conn("a", "main", "b")}}
	if timeout != nil {
		wf.Settings = map[string]interface{}{"executionTimeout": timeout}
	}
	return wf
}

func sleepRegistry(d time.Duration) *workflow.NodeTypeRegistry {
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("test.sleep", func() workflow.NodeExecutor { return sleepNode{d: d} })
	return reg
}

func TestExecutionTimeout_EndsRunCancelledWithTimeout(t *testing.T) {
	start := time.Now()
	err := runWF(t, sleepRegistry(5*time.Second), timeoutWorkflow(0.2), &recStore{}, "")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("run took %v, the timeout did not stop it", elapsed)
	}
	if !errors.Is(err, workflow.ErrExecutionCancelled) || !errors.Is(err, workflow.ErrExecutionTimeout) {
		t.Fatalf("err = %v, want a cancel that is also a timeout", err)
	}
	if err.Error() != "execution timeout after 0.2s" {
		t.Errorf("message = %q", err.Error())
	}
	if c := workflow.ClassifyError(err); c != workflow.ClassTimeout {
		t.Errorf("class = %q, want timeout", c)
	}
	if c := workflow.ClassifyMessage(err.Error()); c != workflow.ClassTimeout {
		t.Errorf("message class = %q, want timeout", c)
	}
}

func TestExecutionTimeout_NumericString(t *testing.T) {
	err := runWF(t, sleepRegistry(5*time.Second), timeoutWorkflow("1"), &recStore{}, "")
	if !errors.Is(err, workflow.ErrExecutionTimeout) || err.Error() != "execution timeout after 1s" {
		t.Fatalf("err = %v, want execution timeout after 1s", err)
	}
}

// Unset, zero, negative and unparsable values keep today's behaviour: no
// timeout at all.
func TestExecutionTimeout_OffByDefault(t *testing.T) {
	for _, v := range []interface{}{nil, 0, 0.0, -1.0, "", "0", "soon", true} {
		if err := runWF(t, sleepRegistry(30*time.Millisecond), timeoutWorkflow(v), &recStore{}, ""); err != nil {
			t.Errorf("executionTimeout=%v: run failed: %v", v, err)
		}
	}
}

// Time spent WAITING does not count: only active running time does, summed
// over the segments before and after a pause.
func TestExecutionTimeout_CountsActiveTimeAcrossPause(t *testing.T) {
	wf := timeoutWorkflow(1)
	wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{ID: "p", Type: "test.pause", Name: "P"})
	wf.Connections = []workflow.WorkflowConnection{conn("t", "main", "a"), conn("a", "main", "p"), conn("p", "main", "b")}

	run := func(d time.Duration, resume string) (string, error) {
		reg := sleepRegistry(d)
		pauseAt := 1
		if resume != "" {
			pauseAt = 0 // the resumed pause node passes its items on
		}
		reg.Register("test.pause", func() workflow.NodeExecutor { return &pauseOnce{at: pauseAt} })
		store := &recStore{}
		err := runWF(t, reg, wf, store, resume)
		return store.waiting, err
	}

	// 0.6s active, then a pause.
	state, err := run(600*time.Millisecond, "")
	if !errors.Is(err, workflow.ErrExecutionPaused) {
		t.Fatalf("first segment: %v, want paused", err)
	}
	time.Sleep(500 * time.Millisecond) // waiting: not counted

	// Another 0.6s after the resume makes 1.2s active, past the 1s budget.
	if _, err := run(600*time.Millisecond, state); !errors.Is(err, workflow.ErrExecutionTimeout) {
		t.Fatalf("resume past the budget: %v, want timeout", err)
	}
	// A short resumed segment fits in what is left, though the wall clock
	// since the start is past 1s.
	if _, err := run(50*time.Millisecond, state); err != nil {
		t.Fatalf("resume within the budget: %v", err)
	}
}
