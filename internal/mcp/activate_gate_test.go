package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// An agent that saves a workflow through a tool cannot switch it on: nobody approved the definition,
// and activating arms its triggers. After the operator approves it (`workflow activate`, or a save
// from the app), the same call goes through.
func TestSetActiveRefusesADefinitionNobodyApproved(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	doc := map[string]interface{}{
		"name": "agent made",
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Trigger", "config": map[string]interface{}{}},
		},
	}
	var saved savedWorkflow
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": doc}, &saved)

	args, _ := json.Marshal(map[string]interface{}{"workflow_id": saved.ID, "active": true})
	_, err := callToolDefault(context.Background(), s, "workflow_set_active", args)
	if err == nil || !strings.Contains(err.Error(), "not approved by you") {
		t.Fatalf("an unapproved activation through a tool: %v", err)
	}
	text := callInto(t, s, "workflow_get", map[string]interface{}{"id": saved.ID}, nil)
	var got struct {
		IsActive bool `json:"is_active"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil || got.IsActive {
		t.Fatalf("a refused activation switched the workflow on: %s (%v)", text, err)
	}

	approveForTest(t, s, saved.ID)
	if _, err := callToolDefault(context.Background(), s, "workflow_set_active", args); err != nil {
		t.Fatalf("an approved definition: %v", err)
	}
}
