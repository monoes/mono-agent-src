package workflow_test

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Expressions a node evaluates itself (core.set assignments, if/filter
// conditions, http.request per-item fields) see the same $trigger, $vars
// and $prevNode as config templates the engine resolves.
func TestNodeEvaluatedExpressionsSeeRunVariables(t *testing.T) {
	done := newRec()
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("test.emit", func() workflow.NodeExecutor { return emitNode{n: 1} })
	reg.Register("core.set", func() workflow.NodeExecutor { return &control.SetNode{} })
	reg.Register("test.done", func() workflow.NodeExecutor { return doneRec{done} })
	assign := func(field, value string) map[string]interface{} {
		return map[string]interface{}{"field": field, "value": value, "type": "string"}
	}
	wf := &workflow.Workflow{ID: "wf", Settings: map[string]interface{}{"variables": map[string]interface{}{"greeting": "hi"}},
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "T"},
			{ID: "f", Type: "test.emit", Name: "F"},
			{ID: "s", Type: "core.set", Name: "S", Config: map[string]interface{}{"assignments": []interface{}{
				assign("trig", "{{ $trigger.who }}"), assign("var", "{{ $vars.greeting }}"), assign("prev", "{{ $prevNode }}"),
			}}},
			{ID: "d", Type: "test.done", Name: "D"},
		}, Connections: []workflow.WorkflowConnection{conn("t", "main", "f"), conn("f", "main", "s"), conn("s", "main", "d")}}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &workflow.WorkflowExecution{ID: "e", WorkflowID: wf.ID, TriggerData: map[string]interface{}{"who": "alice"}}
	if err := workflow.RunExecution(context.Background(), exec, wf, dag, reg, &recStore{}, nil, workflow.NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(*done.items) != 1 {
		t.Fatalf("done got %d items, want 1", len(*done.items))
	}
	got := (*done.items)[0].JSON
	for k, want := range map[string]string{"trig": "alice", "var": "hi", "prev": "F"} {
		if got[k] != want {
			t.Errorf("%s = %#v, want %q", k, got[k], want)
		}
	}
}
