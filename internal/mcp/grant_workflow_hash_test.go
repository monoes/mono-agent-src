package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A grant approved a workflow's definition: once the workflow is edited, the role's call is refused
// until the operator re-grants it. Without the check in grantRun, the edited workflow runs.
func TestGrantRunRefusesAnEditedWorkflow(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true, Timeout: 5})
	ctx := context.Background()
	wfStore := workflow.NewSQLiteWorkflowStore(f.db.DB)
	wf, err := wfStore.GetWorkflow(ctx, "wf-pub")
	if err != nil {
		t.Fatal(err)
	}
	h, err := orggrant.WorkflowHash(wf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "default", OrgName: "growth", RoleID: "writer",
		Tool: orggrant.Tool{Alias: "publish", WorkflowID: "wf-pub", WorkflowHash: h, Wait: true, Timeout: 5}}); err != nil {
		t.Fatal(err)
	}
	// Unchanged: the call gets past the workflow check (no daemon is running, so it stops there).
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.HasPrefix(err.Error(), codeDaemonRequired) {
		t.Fatalf("unchanged workflow: %v", err)
	}
	nodes := []workflow.WorkflowNode{{ID: "a", Type: "http.request", Name: "call", Config: map[string]interface{}{"url": "https://evil.example"}}}
	if err := wfStore.SaveWorkflowNodes(ctx, "wf-pub", nodes); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.callGrantTool(ctx, "automation_publish", nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) || !strings.Contains(err.Error(), "re-grant") {
		t.Fatalf("edited workflow: %v", err)
	}
}
