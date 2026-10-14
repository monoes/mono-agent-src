package workflow_test

import (
	"context"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/workflow"
)

// prevNodeRec records the resolved "prev" config field of each run.
type prevNodeRec struct {
	mu  *sync.Mutex
	got *[]string
}

func (prevNodeRec) Type() string { return "test.prev" }
func (p prevNodeRec) Execute(_ context.Context, in workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, _ := config["prev"].(string)
	*p.got = append(*p.got, s)
	return []workflow.NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

func prevRegistry() (*workflow.NodeTypeRegistry, func() []string) {
	rec := prevNodeRec{mu: &sync.Mutex{}, got: &[]string{}}
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("test.prev", func() workflow.NodeExecutor { return rec })
	reg.Register("test.emit", func() workflow.NodeExecutor { return emitNode{n: 3} })
	reg.Register(workflow.LoopNodeType, func() workflow.NodeExecutor { return &control.SplitInBatchesNode{} })
	return reg, func() []string {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return append([]string(nil), *rec.got...)
	}
}

var prevCfg = map[string]interface{}{"prev": "{{ $prevNode }}"}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// $prevNode names the one node that fed this run; with several feeding it,
// it is empty.
func TestPrevNode_SingleAndMergedInput(t *testing.T) {
	reg, got := prevRegistry()
	wf := &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "Start"},
		{ID: "a", Type: "test.prev", Name: "A", Config: prevCfg},
		{ID: "b", Type: "test.prev", Name: "B", Config: prevCfg},
		{ID: "c", Type: "test.prev", Name: "C", Config: prevCfg},
	}, Connections: []workflow.WorkflowConnection{
		conn("t", "main", "a"), conn("t", "main", "b"),
		conn("a", "main", "c"), conn("b", "main", "c"),
	}}
	if err := runWF(t, reg, wf, &recStore{}, ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	if g := got(); !eqStrs(g, []string{"Start", "Start", ""}) {
		t.Fatalf("$prevNode per run = %q, want [Start Start \"\"]", g)
	}
}

// In a loop the body sees the loop node, and the loop node's later runs see
// the body node that fed it back.
func TestPrevNode_Loop(t *testing.T) {
	reg, got := prevRegistry()
	wf := &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "Start"},
		{ID: "f", Type: "test.emit", Name: "Fetch"},
		{ID: "s", Type: workflow.LoopNodeType, Name: "Loop", Config: map[string]interface{}{"batch_size": 2}},
		{ID: "b", Type: "test.prev", Name: "Body", Config: prevCfg},
		{ID: "d", Type: "test.prev", Name: "Done", Config: prevCfg},
	}, Connections: []workflow.WorkflowConnection{
		conn("t", "main", "f"), conn("f", "main", "s"),
		conn("s", "loop", "b"), conn("b", "main", "s"),
		conn("s", "done", "d"),
	}}
	if err := runWF(t, reg, wf, &recStore{}, ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	if g := got(); !eqStrs(g, []string{"Loop", "Loop", "Loop"}) {
		t.Fatalf("$prevNode per run = %q, want [Loop Loop Loop]", g)
	}
}
