package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The per-profile engine outlives the request that first started it: two
// sequential runs on one profile both execute even though the first request's
// context was cancelled when it ended.
func TestEngineOutlivesTheRequestThatStartedIt(t *testing.T) {
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")
	wf := &workflow.Workflow{
		ProfileID: "default", ID: "wf-twice", Name: "twice", IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}},
	}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		r := httptest.NewRequest("POST", "/profiles/default/workflows/wf-twice/run", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+tok)
		rw := httptest.NewRecorder()
		s.Handler().ServeHTTP(rw, r)
		cancel() // the request is over
		if rw.Code != http.StatusOK {
			t.Fatalf("run %d: status %d: %s", i, rw.Code, rw.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rw.Body.Bytes(), &out)
		if out["timed_out"] == true || out["status"] != "SUCCESS" {
			t.Fatalf("run %d did not execute: %s", i, rw.Body.String())
		}
	}
}
