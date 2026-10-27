package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The API cannot switch on a definition the operator has not approved: activating arms triggers, and
// the server process's own marker says nothing about who is calling it. A refused activation is a
// 403 and changes nothing; once the operator has approved the definition (`workflow activate`, or a
// save from the app) the same request goes through, and deactivating is never refused.
func TestActivateRefusesAnUnapprovedDefinition(t *testing.T) {
	t.Setenv("MONOAGENT_WEBHOOK_ADDR", "127.0.0.1:0")
	s := newTestServer(t, true)
	ctx := context.Background()
	tok := testToken(t, s, "default")
	base := "/profiles/default/workflows/"

	wf := &workflow.Workflow{ID: "agent-made", Name: "agent made", ProfileID: "default",
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}}}
	if err := s.rt.store.CreateWorkflow(ctx, wf); err != nil { // not approving: an agent's save
		t.Fatal(err)
	}

	rw := doReq(t, s, "POST", base+wf.ID+"/activate", tok, nil)
	if rw.Code != http.StatusForbidden || !strings.Contains(rw.Body.String(), "not approved by you") {
		t.Fatalf("activate of an unapproved definition: %d %s", rw.Code, rw.Body.String())
	}
	if got, _ := s.rt.store.GetWorkflow(ctx, wf.ID); got == nil || got.IsActive {
		t.Fatalf("a refused activation switched the workflow on: %+v", got)
	}

	if err := s.rt.store.ApproveDefinition(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	if rw := doReq(t, s, "POST", base+wf.ID+"/activate", tok, nil); rw.Code != http.StatusOK {
		t.Fatalf("activate of an approved definition: %d %s", rw.Code, rw.Body.String())
	}
	if rw := doReq(t, s, "POST", base+wf.ID+"/deactivate", tok, nil); rw.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", rw.Code, rw.Body.String())
	}
}
