package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A key holder can be an agent: a reply token in a run's data stays out of
// the run and executions routes (X-Full-Outputs or not) and of the HIL list,
// and an approval whose data holds the placeholder is refused.
func TestRoutesHideReplyTokensInRunData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19725")
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	wf := &workflow.Workflow{ProfileID: "default", ID: "wf-resume", Name: "resume", IsActive: true,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Start"},
			{ID: "s", Type: "core.set", Name: "Keep", Config: map[string]interface{}{"resume": "{{ $execution.resumeUrl }}"}},
			{ID: "w", Type: "core.wait", Name: "Wait", Config: map[string]interface{}{"resume": "webhook"}},
			{ID: "m", Type: "comm.email_send", Name: "Mail", Config: map[string]interface{}{"to": "ada@example.test", "subject": "Hi", "body": "{{ $json.resume }}"}},
		},
		Connections: []workflow.WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "s", TargetHandle: "main"},
			{SourceNodeID: "s", SourceHandle: "main", TargetNodeID: "w", TargetHandle: "main"},
			{SourceNodeID: "w", SourceHandle: "main", TargetNodeID: "m", TargetHandle: "main"},
		}}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/profiles/default/workflows/wf-resume/run", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(fullOutputsHeader, "1")
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, req)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"WAITING"`) {
		t.Fatalf("run: %d %s", r.Code, r.Body.String())
	}
	db := s.rt.db.DB
	var execID, token string
	if err := db.QueryRow(`SELECT execution_id, token FROM wait_replies WHERE workflow_id = 'wf-resume'`).Scan(&execID, &token); err != nil {
		t.Fatal(err)
	}
	hidden := "http://127.0.0.1:19725/reply/" + workflow.ReplyTokenHidden
	if strings.Contains(r.Body.String(), token) || !strings.Contains(r.Body.String(), hidden) {
		t.Errorf("run:\n%s", r.Body.String())
	}

	resume := "http://127.0.0.1:19725/reply/" + token
	if _, err := db.Exec(`UPDATE workflow_executions SET trigger_data = ?, error_message = ? WHERE id = ?`,
		`{"callback":"`+resume+`"}`, "posting "+resume+" failed", execID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		VALUES ('h1', ?, 'wf-resume', 'n', 'Review', 'pending', ?, '{}', '{"outbound":false}', 'default')`, execID, `{"link":"`+resume+`"}`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/profiles/default/workflows/wf-resume/executions", "/profiles/default/hil"} {
		rw := doReq(t, s, "GET", path, tok, nil)
		if rw.Code != http.StatusOK || strings.Contains(rw.Body.String(), token) || !strings.Contains(rw.Body.String(), hidden) {
			t.Errorf("GET %s: %d %s", path, rw.Code, rw.Body.String())
		}
	}
	rw := doReq(t, s, "POST", "/profiles/default/hil/h1/approve", tok, []byte(`{"data":{"link":"`+hidden+`"}}`))
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "leave out the fields") {
		t.Errorf("approve with the placeholder: %d %s", rw.Code, rw.Body.String())
	}
}
