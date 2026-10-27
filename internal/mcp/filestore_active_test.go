package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A workflow saved through workflow_save lives as a file (the file store
// wins over SQLite): workflow_set_active and workflow_delete must change the
// file too. Before, activation only updated SQLite (the workflow stayed
// inactive and could not run) and delete left the file, so it was still listed.
func TestSetActiveAndDeleteReachTheWorkflowFile(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	doc := map[string]interface{}{
		"name": "file backed",
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Trigger", "config": map[string]interface{}{}},
		},
	}
	var saved savedWorkflow
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": doc}, &saved)
	approveForTest(t, s, saved.ID) // the operator approved it: a tool cannot switch on what nobody approved
	callInto(t, s, "workflow_set_active", map[string]interface{}{"workflow_id": saved.ID, "active": true}, nil)
	text := callInto(t, s, "workflow_get", map[string]interface{}{"id": saved.ID}, nil)
	var got struct {
		IsActive bool `json:"is_active"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if !got.IsActive {
		t.Fatalf("workflow_set_active did not activate the saved workflow: %s", text)
	}

	callInto(t, s, "workflow_delete", map[string]interface{}{"workflow_id": saved.ID}, nil)
	if list := callInto(t, s, "workflow_list", map[string]interface{}{}, nil); strings.Contains(list, saved.ID) {
		t.Fatalf("deleted workflow still listed: %s", list)
	}
}
