package mcp

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The review's reproduction over MCP: a node puts the run's resume URL in its
// data. workflow_run and workflow_status return every node's items, and an
// agent reaches this server, so neither has the reply token.
func TestWorkflowRunHidesAResumeURLInNodeData(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19723")
	s := newTestServerAllowMutations(t, true)
	var saved struct {
		ID string `json:"id"`
	}
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{
		"name": "resume-in-data", "is_active": true,
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Start", "config": map[string]interface{}{}},
			{"id": "s", "node_type": "core.set", "name": "Keep", "config": map[string]interface{}{"resume": "{{ $execution.resumeUrl }}"}},
			{"id": "w", "node_type": "core.wait", "name": "Wait", "config": map[string]interface{}{"resume": "webhook"}},
			{"id": "m", "node_type": "comm.email_send", "name": "Mail", "config": map[string]interface{}{"to": "ada@example.test", "subject": "Hi", "body": "{{ $json.resume }}"}},
		},
		"connections": []map[string]interface{}{
			{"id": "c1", "source_node_id": "t", "source_handle": "main", "target_node_id": "s", "target_handle": "main"},
			{"id": "c2", "source_node_id": "s", "source_handle": "main", "target_node_id": "w", "target_handle": "main"},
			{"id": "c3", "source_node_id": "w", "source_handle": "main", "target_node_id": "m", "target_handle": "main"},
		},
	}}, &saved)
	activateForTest(t, s, saved.ID)

	var run struct {
		ExecutionID string `json:"execution_id"`
		Status      string `json:"status"`
	}
	ran := callInto(t, s, "workflow_run", map[string]interface{}{"id": saved.ID, "timeout_seconds": 30}, &run)
	if run.Status != "WAITING" {
		t.Fatalf("workflow_run = %s", ran)
	}
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	var token string
	if err := rt.db.DB.QueryRow(`SELECT token FROM wait_replies WHERE execution_id = ?`, run.ExecutionID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	hidden := "http://127.0.0.1:19723/reply/" + workflow.ReplyTokenHidden
	if strings.Contains(ran, token) || !strings.Contains(ran, hidden) {
		t.Errorf("workflow_run:\n%s", ran)
	}
	status := callInto(t, s, "workflow_status", map[string]interface{}{"execution_id": run.ExecutionID}, nil)
	if strings.Contains(status, token) || !strings.Contains(status, hidden) {
		t.Errorf("workflow_status:\n%s", status)
	}
}

// node_run returns a node's outputs and hil_list an item's data: a reply
// link in either keeps its token out, and hil_approve refuses data that
// holds the placeholder, so it can't replace the link the run needs.
func TestNodeRunAndHILListHideReplyTokens(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19723")
	s := newTestServerAllowMutations(t, true)
	link := "http://127.0.0.1:19723/reply/" + strings.Repeat("k", 43)
	hidden := "http://127.0.0.1:19723/reply/" + workflow.ReplyTokenHidden
	out := callInto(t, s, "node_run", map[string]interface{}{"type": "core.set", "config": map[string]interface{}{"note": "hi"},
		"input": map[string]interface{}{"link": link}}, nil)
	if strings.Contains(out, link) || !strings.Contains(out, hidden) {
		t.Errorf("node_run:\n%s", out)
	}

	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.db.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		VALUES ('h1', 'ex-1', 'wf-1', 'n', 'Review', 'pending', ?, '{}', '{"outbound":false}', ?)`, `{"link":"`+link+`"}`, testProfile); err != nil {
		t.Fatal(err)
	}
	if list := callInto(t, s, "hil_list", map[string]interface{}{}, nil); strings.Contains(list, link) || !strings.Contains(list, hidden) {
		t.Errorf("hil_list:\n%s", list)
	}
	wantErr(t, s, "hil_approve", map[string]interface{}{"id": "h1", "data": map[string]interface{}{"link": hidden}}, "leave out the fields")
}
