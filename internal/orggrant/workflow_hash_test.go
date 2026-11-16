package orggrant

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func hashWF(cfg map[string]interface{}) *workflow.Workflow {
	return &workflow.Workflow{ID: "wf-1", Name: "n", Nodes: []workflow.WorkflowNode{
		{ID: "a", Type: "http.request", Name: "call", Config: cfg},
	}}
}

func TestApprovedWorkflowEditsInvalidate(t *testing.T) {
	base := map[string]interface{}{"url": "https://a.example", "body": "x", "script": "return 1"}
	h, err := WorkflowHash(hashWF(base))
	if err != nil || h == "" {
		t.Fatalf("hash = %q, %v", h, err)
	}
	tool := Tool{WorkflowHash: h}
	if err := tool.CheckWorkflow(hashWF(base)); err != nil {
		t.Fatalf("unchanged workflow refused: %v", err)
	}
	for _, key := range []string{"url", "body", "script"} {
		cfg := map[string]interface{}{"url": "https://a.example", "body": "x", "script": "return 1"}
		cfg[key] = "changed"
		if err := tool.CheckWorkflow(hashWF(cfg)); err == nil {
			t.Errorf("edit of %s was honoured", key)
		}
	}
}

func TestWorkflowActiveFlagDoesNotInvalidate(t *testing.T) {
	cfg := map[string]interface{}{"url": "u"}
	h, _ := WorkflowHash(hashWF(cfg))
	wf := hashWF(cfg)
	wf.IsActive = true
	if err := (Tool{WorkflowHash: h}).CheckWorkflow(wf); err != nil {
		t.Fatalf("activation invalidated the grant: %v", err)
	}
}

// A grant made before the hash existed keeps working until its next re-grant.
func TestLegacyGrantWithoutHashStillHonoured(t *testing.T) {
	if err := (Tool{}).CheckWorkflow(hashWF(map[string]interface{}{"url": "z"})); err != nil {
		t.Fatalf("legacy grant refused: %v", err)
	}
}

func TestHashSurvivesStoreRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	g, err := s.UpsertGrant(context.Background(), GrantInput{ProfileID: "default", OrgName: "growth", RoleID: "lead",
		Tool: Tool{Alias: "a", WorkflowID: "wf-1", WorkflowHash: "abc"}})
	if err != nil || g.Automation().WorkflowHash != "abc" {
		t.Fatalf("grant = %+v, %v", g, err)
	}
}
