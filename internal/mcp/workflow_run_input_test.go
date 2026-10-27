package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// workflow_run's input can't plant the engine's marks (who started the
// run, which call it is), and still passes several items with _items.
func TestWorkflowRunInputCantPlantTheEnginesMarks(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	wf := &workflow.Workflow{ProfileID: "default", ID: "wf-in", Name: "in", IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Start"}}}
	if err := rt.store.CreateWorkflow(workflow.WithApproval(ctx), wf); err != nil {
		t.Fatal(err)
	}
	if _, err := callToolDefault(ctx, s, "workflow_run", json.RawMessage(
		`{"id":"wf-in","input":{"keep":"yes","_ai_caller":"unapproved-workflow","_unapproved_caller":"wf-x","_subworkflow":{},"_items":[{"n":1},{"n":2}]}}`)); err != nil {
		t.Fatal(err)
	}
	var raw, out string
	if err := rt.db.DB.QueryRow(`SELECT e.trigger_data, n.output_items FROM workflow_executions e
		JOIN workflow_execution_nodes n ON n.execution_id = e.id AND n.node_id = 't' WHERE e.workflow_id = 'wf-in'`).Scan(&raw, &out); err != nil {
		t.Fatal(err)
	}
	var td map[string]interface{}
	var items []workflow.Item
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{workflow.AICallerKey, workflow.UnapprovedCallerKey, workflow.SubWorkflowParentKey} {
		if _, ok := td[k]; ok {
			t.Errorf("the input planted %s: %v", k, td)
		}
	}
	if td["keep"] != "yes" || len(items) != 2 || items[1].JSON["n"] != float64(2) {
		t.Errorf("trigger data %v, the trigger's items %v", td, items)
	}
}
