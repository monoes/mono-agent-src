package control

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// makeBinNode attaches a file to every item.
type makeBinNode struct{}

func (makeBinNode) Type() string { return "test.makebin" }
func (makeBinNode) Execute(_ context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	out := make([]workflow.Item, 0, len(in.Items))
	for _, it := range in.Items {
		out = append(out, workflow.Item{JSON: copyMap(it.JSON), Binary: map[string][]byte{"file": {0, 1, 2, 0xff}}})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// readBinNode reports what it received: the file's bytes and whether the
// JSON still carries anything under a reserved key.
type readBinNode struct{}

func (readBinNode) Type() string { return "test.readbin" }
func (readBinNode) Execute(_ context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	out := make([]workflow.Item, 0, len(in.Items))
	for _, it := range in.Items {
		m := map[string]interface{}{"file": it.Binary["file"], "keys": len(it.JSON)}
		out = append(out, workflow.NewItem(m))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func TestExecuteWorkflow_PassesBinaryToChild(t *testing.T) {
	f := newSubflowFixture(t)
	f.chain(t, "child", workflow.TriggerTypeExecuteWorkflow, false, workflow.WorkflowNode{Type: "test.readbin"})
	f.chain(t, "parent", "trigger.manual", true,
		workflow.WorkflowNode{Type: "test.makebin"},
		execNode(map[string]interface{}{"workflowId": "child", "mode": "each"}))

	exec, _ := f.runParent(t, "parent")
	if exec.Status != "SUCCESS" {
		t.Fatalf("parent status = %s (%s), want SUCCESS", exec.Status, exec.ErrorMessage)
	}
	out := nodeRecord(t, exec, "parent-1").OutputItems
	if len(out) != 3 {
		t.Fatalf("output items = %d, want 3", len(out))
	}
	for i, it := range out {
		// Persisted []byte reads back as base64 text.
		if it.JSON["file"] != "AAEC/w==" {
			t.Errorf("item %d file = %v, want the parent's bytes", i, it.JSON["file"])
		}
		if it.JSON["keys"] != 1.0 {
			t.Errorf("item %d JSON had %v keys, want only n", i, it.JSON["keys"])
		}
	}
}
