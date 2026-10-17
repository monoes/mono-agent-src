package noderegistry

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow whose app node carries a key a run would refuse fails
// `workflow validate` (workflow.ValidateWorkflow) ahead of any run, with
// the run's message and the node.
func TestValidateReportsAnUndeclaredKey(t *testing.T) {
	Build(nil)
	wf := &workflow.Workflow{ID: "wf1", Name: "w", Nodes: []workflow.WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "T"},
		{ID: "upd", Type: "app.zammad", Name: "Update ticket", Config: map[string]interface{}{"resource": "ticket",
			"operation": "update", "id": "5", "updateFields": map[string]interface{}{"article": map[string]interface{}{"type": "email"}}}},
	}, Connections: []workflow.WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "upd", TargetHandle: "main"}}}
	rep := workflow.ValidateWorkflow(wf, workflow.ValidateOptions{})
	want := `node "upd": apipkg zammad ticket.update: updateFields has no option "article"`
	if rep.Valid || !strings.Contains(strings.Join(rep.Errors, "\n"), want) {
		t.Errorf("valid=%v errors %q; want %q", rep.Valid, rep.Errors, want)
	}
}

// An n8n workflow whose node carries a key its app doesn't declare is
// imported with the problem listed, the run's message and the node, and
// the import is not complete; `workflow validate` then fails on it.
func TestN8nImportListsAnUndeclaredKey(t *testing.T) {
	Build(nil)
	raw := []byte(`{"name":"Z","nodes":[
	  {"id":"1","name":"Start","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	  {"id":"2","name":"Update user","type":"n8n-nodes-base.zammad","typeVersion":1,"position":[200,0],
	   "parameters":{"resource":"user","operation":"update","id":"5","updateFields":{"invite":true}}}],
	 "connections":{"Start":{"main":[[{"node":"Update user","type":"main","index":0}]]}}}`)
	wf, rep, err := n8nimport.Convert(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Complete || !strings.Contains(strings.Join(rep.Validation, "\n"), `updateFields has no option "invite"`) {
		t.Fatalf("complete=%v validation %q", rep.Complete, rep.Validation)
	}
	found := false
	for _, n := range wf.Nodes {
		found = found || n.Type == "app.zammad"
	}
	if !found {
		t.Errorf("the import dropped the node: %+v", wf.Nodes)
	}
}

// Activation refuses a workflow a run would refuse, naming the node and
// the key, and leaves it inactive: it shows up then, not in its first
// scheduled run.
func TestActivationRefusesAnUndeclaredKey(t *testing.T) {
	db := sendsDB(t)
	reg := Build(db)
	eng := workflow.NewWorkflowEngineWithStore(workflow.NewSQLiteWorkflowStore(db), db, nil, reg,
		workflow.EngineConfig{ProfileID: "default"}, zerolog.Nop())
	t.Cleanup(func() { _ = eng.Stop() })
	ctx := sendsCtx(db)
	wf := &workflow.Workflow{ID: "wf-bad", Name: "bad", ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "t", WorkflowID: "wf-bad", Type: "trigger.manual", Name: "T"},
		{ID: "upd", WorkflowID: "wf-bad", Type: "app.zammad", Name: "Update ticket", Config: map[string]interface{}{"resource": "ticket",
			"operation": "update", "id": "5", "updateFields": map[string]interface{}{"articles": []interface{}{map[string]interface{}{"body": "x"}}}}},
	}, Connections: []workflow.WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "upd", TargetHandle: "main"}}}
	if err := eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	err := eng.ActivateWorkflow(ctx, "wf-bad")
	if err == nil || !strings.Contains(err.Error(), `node upd (Update ticket): apipkg zammad ticket.update: updateFields has no option "articles"`) {
		t.Fatalf("activate: %v", err)
	}
	if got, err := eng.GetWorkflow(ctx, "wf-bad"); err != nil || got.IsActive {
		t.Errorf("after a refused activation: %+v, %v", got, err)
	}
}
