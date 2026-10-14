package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// An agent building a workflow over MCP must find a trigger with node_list
// and add it with workflow_node_add: triggers are not in the node registry.
func TestTriggersAreListedAndAddable(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	text := callInto(t, s, "node_list", map[string]interface{}{"category": "trigger"}, nil)
	var nodes []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(text), &nodes); err != nil {
		t.Fatalf("decode node_list: %v", err)
	}
	got := map[string]bool{}
	for _, n := range nodes {
		got[n.Type] = true
	}
	for _, want := range []string{"trigger.manual", "trigger.schedule", "trigger.webhook"} {
		if !got[want] {
			t.Errorf("node_list category trigger lacks %s (got %v)", want, nodes)
		}
	}

	var created struct {
		WorkflowID string `json:"workflow_id"`
	}
	callInto(t, s, "workflow_create", map[string]interface{}{"name": "with trigger"}, &created)
	var n struct {
		NodeID string `json:"node_id"`
	}
	callInto(t, s, "workflow_node_add", map[string]interface{}{"workflow_id": created.WorkflowID, "node_type": "trigger.manual", "name": "Start"}, &n)
	if n.NodeID == "" {
		t.Fatal("workflow_node_add trigger.manual returned no node id")
	}
	_, err := callNamed(t, s, "workflow_node_add", map[string]interface{}{"workflow_id": created.WorkflowID, "node_type": "trigger.nope", "name": "X"})
	if err == nil || !strings.Contains(err.Error(), "node_list") {
		t.Errorf("unknown type error should point at node_list, got %v", err)
	}
}

func TestStringListAcceptsOneString(t *testing.T) {
	var a struct {
		G stringList `json:"g"`
		H stringList `json:"h"`
	}
	if err := json.Unmarshal([]byte(`{"g":"core","h":["a","b"]}`), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.G) != 1 || a.G[0] != "core" || len(a.H) != 2 {
		t.Fatalf("got %v %v", a.G, a.H)
	}
}
