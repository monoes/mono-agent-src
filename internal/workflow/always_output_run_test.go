package workflow_test

import (
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A node that emits nothing stops the branch unless always_output_data is
// set: then the next node runs once with an empty item.
func TestAlwaysOutputDataKeepsTheBranchRunning(t *testing.T) {
	for _, on := range []bool{false, true} {
		next := newRec()
		reg := workflow.NewNodeTypeRegistry()
		reg.Register("test.emit", func() workflow.NodeExecutor { return emitNode{n: 0} })
		reg.Register("test.rec", func() workflow.NodeExecutor { return next })
		cfg := map[string]interface{}{}
		if on {
			cfg["always_output_data"] = true
		}
		wf := &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "T"},
			{ID: "e", Type: "test.emit", Name: "Empty", Config: cfg},
			{ID: "n", Type: "test.rec", Name: "Next"},
		}, Connections: []workflow.WorkflowConnection{conn("t", "main", "e"), conn("e", "main", "n")}}
		if err := runWF(t, reg, wf, &recStore{}, ""); err != nil {
			t.Fatalf("on=%v: %v", on, err)
		}
		got := next.snapshot()
		if on && !eqInts(got, []int{1}) {
			t.Errorf("always_output_data: next ran %v, want once with one item", got)
		}
		if !on && len(got) != 0 {
			t.Errorf("default: next ran %v, want not at all", got)
		}
	}
}
