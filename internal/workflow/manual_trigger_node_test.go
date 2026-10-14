package workflow

import (
	"context"
	"testing"
)

// A manual run of a workflow with a manual and a webhook trigger feeding the
// same node fires only the manual trigger; the webhook trigger would add an
// empty item to that node's input.
func TestManualRunFiresOnlyTheManualTrigger(t *testing.T) {
	ctx := context.Background()
	store := newFullEngineStore(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	wf := &Workflow{ID: "wf-two", Name: "two", ProfileID: "default", Nodes: []WorkflowNode{
		{ID: "hook", WorkflowID: "wf-two", Type: TriggerNodeTypeWebhook, Name: "Hook"},
		{ID: "man", WorkflowID: "wf-two", Type: "trigger.manual", Name: "Manual"},
	}}
	if err := eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowActive(ctx, wf.ID, true); err != nil {
		t.Fatal(err)
	}
	exec, err := eng.newManualExecution(ctx, wf.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if exec.TriggerNodeID != "man" {
		t.Errorf("TriggerNodeID = %q, want the manual trigger", exec.TriggerNodeID)
	}
	for _, c := range []struct {
		nodes []WorkflowNode
		want  string
	}{
		{[]WorkflowNode{{ID: "hook", Type: TriggerNodeTypeWebhook}}, ""},
		{[]WorkflowNode{{ID: "a", Type: "trigger.manual"}, {ID: "b", Type: "trigger.manual"}}, ""},
		{[]WorkflowNode{{ID: "a", Type: "trigger.manual", Disabled: true}, {ID: "b", Type: "trigger.manual"}}, "b"},
	} {
		if got := manualTriggerNode(&Workflow{Nodes: c.nodes}); got != c.want {
			t.Errorf("manualTriggerNode(%v) = %q, want %q", c.nodes, got, c.want)
		}
	}
}
