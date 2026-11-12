package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The caller of workflow_run is an agent, not the user's terminal: the run carries an agent caller
// mark, as `workflow run` gives an agent's run, so a node that refuses an AI run (ai.task opens a coder
// task) refuses it. Without the mark the run counted as the user's own.
func TestWorkflowRunMarksTheRunAsAnAgentCaller(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	var saved struct {
		ID string `json:"id"`
	}
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{
		"name": "caller-mark", "is_active": true,
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Start", "config": map[string]interface{}{}},
			{"id": "a", "node_type": "ai.task", "name": "Task", "config": map[string]interface{}{"goal": "do a thing"}},
		},
		"connections": []map[string]interface{}{
			{"id": "c1", "source_node_id": "t", "source_handle": "main", "target_node_id": "a", "target_handle": "main"},
		},
	}}, &saved)
	activateForTest(t, s, saved.ID)

	var run struct {
		ExecutionID string `json:"execution_id"`
	}
	callInto(t, s, "workflow_run", map[string]interface{}{"id": saved.ID, "timeout_seconds": 30,
		"input": map[string]interface{}{"keep": "yes", workflow.AICallerKey: ""}}, &run)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	var raw, nodeErr string
	if err := rt.db.DB.QueryRow(`SELECT e.trigger_data, COALESCE(n.error_message, '') FROM workflow_executions e
		JOIN workflow_execution_nodes n ON n.execution_id = e.id AND n.node_id = 'a' WHERE e.id = ?`, run.ExecutionID).Scan(&raw, &nodeErr); err != nil {
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
		t.Errorf("ai.task in an MCP-started run: error %q, want the AI-run refusal", nodeErr)
	}
}
