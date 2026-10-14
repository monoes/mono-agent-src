package workflow_test

import (
	"testing"

	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A node that runs on every loop pass must see its whole config each time:
// per-item fields (core.set's assignments[].value) were cut out of the
// workflow's own config map on the first pass, so later passes got nil.
func TestLoop_PerItemConfigSurvivesEveryPass(t *testing.T) {
	done := newRec()
	reg := workflow.NewNodeTypeRegistry()
	reg.Register(workflow.LoopNodeType, func() workflow.NodeExecutor { return &control.SplitInBatchesNode{} })
	reg.Register("test.emit", func() workflow.NodeExecutor { return emitNode{n: 7} })
	reg.Register("core.set", func() workflow.NodeExecutor { return &control.SetNode{} })
	reg.Register("test.done", func() workflow.NodeExecutor { return doneRec{done} })
	assignments := []interface{}{
		map[string]interface{}{"field": "processed", "value": "yes", "type": "string"},
		map[string]interface{}{"field": "copy", "value": "{{ $json.i }}", "type": "number"},
	}
	wf := &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "T"},
		{ID: "f", Type: "test.emit", Name: "F"},
		{ID: "s", Type: workflow.LoopNodeType, Name: "S", Config: map[string]interface{}{"batch_size": 3}},
		{ID: "m", Type: "core.set", Name: "Mark", Config: map[string]interface{}{"assignments": assignments}},
		{ID: "d", Type: "test.done", Name: "D"},
	}, Connections: []workflow.WorkflowConnection{
		conn("t", "main", "f"), conn("f", "main", "s"),
		conn("s", "loop", "m"), conn("m", "main", "s"),
		conn("s", "done", "d"),
	}}
	if err := runWF(t, reg, wf, &recStore{}, ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	items := *done.items
	if len(items) != 7 {
		t.Fatalf("done got %d items, want 7", len(items))
	}
	for _, it := range items {
		if it.JSON["processed"] != "yes" {
			t.Errorf("item %v: processed = %v, want yes on every pass", it.JSON["i"], it.JSON["processed"])
		}
		if it.JSON["copy"] != float64(toInt(it.JSON["i"])) {
			t.Errorf("item %v: copy = %v", it.JSON["i"], it.JSON["copy"])
		}
	}
	if v := assignments[0].(map[string]interface{})["value"]; v != "yes" {
		t.Errorf("the workflow's own config was changed: value = %v", v)
	}
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return -1
}
