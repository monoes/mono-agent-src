package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A run a key holder starts over the HTTP API is not the operator's own: the holder can be an agent, as
// over MCP, so the run carries an agent caller mark, as `workflow run` gives an agent's run. Without
// it a node that refuses an AI run (ai.task opens a coder task) treated the call as the user's.
func TestRunRouteMarksTheRunAsAnAgentCaller(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	wf := &workflow.Workflow{ProfileID: "default", ID: "wf-mark", Name: "mark", IsActive: true,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Start"},
			{ID: "a", Type: "ai.task", Name: "Task", Config: map[string]interface{}{"goal": "do a thing"}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "c", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "a", TargetHandle: "main"}}}
	for i := range wf.Nodes {
		if err := wf.Nodes[i].MarshalConfig(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatal(err)
	}
	r := doReq(t, s, "POST", "/profiles/default/workflows/wf-mark/run", tok, []byte(`{"input":{"keep":"yes"}}`))
	if r.Code != http.StatusOK {
		t.Fatalf("run: %d %s", r.Code, r.Body.String())
	}
	var raw, nodeErr string
	if err := s.rt.db.DB.QueryRow(`SELECT e.trigger_data, COALESCE(n.error_message, '') FROM workflow_executions e
		JOIN workflow_execution_nodes n ON n.execution_id = e.id AND n.node_id = 'a' WHERE e.workflow_id = 'wf-mark'`).Scan(&raw, &nodeErr); err != nil {
		t.Fatal(err)
	}
	var td map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		t.Fatal(err)
	}
	if m, _ := td[workflow.AICallerKey].(string); m == "" || td["keep"] != "yes" {
		t.Errorf("trigger data %v: want an agent caller mark and the input kept", td)
	}
	if !strings.Contains(nodeErr, "isn't the user's own") {
		t.Errorf("ai.task in an API-started run: error %q, want the AI-run refusal", nodeErr)
	}
}
