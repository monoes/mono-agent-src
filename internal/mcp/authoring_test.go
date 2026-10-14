package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// callNamed calls one tool of s directly (no Serve), so that a test can make
// calls that depend on each other against one open runtime.
func callNamed(t *testing.T, s *Server, name string, args interface{}) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return callTool(context.Background(), s, name, b)
}

// callInto is callNamed that fails the test on a tool error and decodes the
// result into out (when out is not nil).
func callInto(t *testing.T, s *Server, name string, args interface{}, out interface{}) string {
	t.Helper()
	text, err := callNamed(t, s, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, text, err)
		}
	}
	return text
}

func wantErr(t *testing.T, s *Server, name string, args interface{}, contains string) {
	t.Helper()
	text, err := callNamed(t, s, name, args)
	if err == nil {
		t.Fatalf("%s: succeeded (%s), want an error that says %q", name, text, contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("%s: %v, want it to say %q", name, err, contains)
	}
}

type savedWorkflow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Nodes       []struct {
		ID     string                 `json:"id"`
		Type   string                 `json:"node_type"`
		Name   string                 `json:"name"`
		Config map[string]interface{} `json:"config"`
	} `json:"nodes"`
	Connections []struct {
		ID     string `json:"id"`
		Source string `json:"source_node_id"`
		Target string `json:"target_node_id"`
		Handle string `json:"target_handle"`
	} `json:"connections"`
}

// newAuthoringWorkflow builds a two-node workflow the way a model does:
// workflow_create, then workflow_node_add twice.
func newAuthoringWorkflow(t *testing.T, s *Server) (wfID, a, b string) {
	t.Helper()
	var created struct {
		WorkflowID string `json:"workflow_id"`
	}
	callInto(t, s, "workflow_create", map[string]interface{}{"name": "wired"}, &created)
	var n1, n2 struct {
		NodeID string `json:"node_id"`
	}
	callInto(t, s, "workflow_node_add", map[string]interface{}{"workflow_id": created.WorkflowID, "node_type": "core.set", "name": "first", "config": map[string]interface{}{"include_input": true}}, &n1)
	callInto(t, s, "workflow_node_add", map[string]interface{}{"workflow_id": created.WorkflowID, "node_type": "core.filter", "name": "second"}, &n2)
	return created.WorkflowID, n1.NodeID, n2.NodeID
}

func getWorkflow(t *testing.T, s *Server, id string) savedWorkflow {
	t.Helper()
	var wf savedWorkflow
	callInto(t, s, "workflow_get", map[string]interface{}{"id": id}, &wf)
	return wf
}

func TestWorkflowConnectAndDisconnect(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	wf, a, b := newAuthoringWorkflow(t, s)

	var conn struct {
		ID           string `json:"id"`
		SourceNodeID string `json:"source_node_id"`
		TargetHandle string `json:"target_handle"`
	}
	callInto(t, s, "workflow_connect", map[string]interface{}{"workflow_id": wf, "from": a, "to": b + ":main"}, &conn)
	if conn.ID == "" || conn.SourceNodeID != a || conn.TargetHandle != "main" {
		t.Fatalf("connection = %+v", conn)
	}
	got := getWorkflow(t, s, wf)
	if len(got.Connections) != 1 || got.Connections[0].Source != a || got.Connections[0].Target != b {
		t.Fatalf("workflow_get after connect: %+v", got.Connections)
	}

	wantErr(t, s, "workflow_connect", map[string]interface{}{"workflow_id": wf, "from": a, "to": "no-such-node"}, `node "no-such-node" not found`)
	wantErr(t, s, "workflow_disconnect", map[string]interface{}{"workflow_id": wf, "connection_id": "nope"}, `connection "nope" not found`)

	callInto(t, s, "workflow_disconnect", map[string]interface{}{"workflow_id": wf, "connection_id": conn.ID}, nil)
	if got := getWorkflow(t, s, wf); len(got.Connections) != 0 {
		t.Fatalf("connections after disconnect: %+v", got.Connections)
	}
}

func TestWorkflowNodeUpdateAndDelete(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	wf, a, b := newAuthoringWorkflow(t, s)
	callInto(t, s, "workflow_connect", map[string]interface{}{"workflow_id": wf, "from": a, "to": b}, nil)

	callInto(t, s, "workflow_node_update", map[string]interface{}{"workflow_id": wf, "node_id": a, "name": "renamed", "config": map[string]interface{}{"assignments": "[]"}, "merge": true}, nil)
	got := getWorkflow(t, s, wf)
	for _, n := range got.Nodes {
		if n.ID == a && (n.Name != "renamed" || n.Config["assignments"] != "[]" || n.Config["include_input"] != true) {
			t.Fatalf("merged update: %+v", n)
		}
	}
	callInto(t, s, "workflow_node_update", map[string]interface{}{"workflow_id": wf, "node_id": a, "config": map[string]interface{}{"only": "this"}}, nil)
	for _, n := range getWorkflow(t, s, wf).Nodes {
		if n.ID == a && (len(n.Config) != 1 || n.Config["only"] != "this") {
			t.Fatalf("a config without merge replaces the config: %+v", n.Config)
		}
	}
	wantErr(t, s, "workflow_node_update", map[string]interface{}{"workflow_id": wf, "node_id": "ghost"}, `node "ghost" not found`)

	var del struct {
		RemovedConnections int `json:"removed_connections"`
	}
	callInto(t, s, "workflow_node_delete", map[string]interface{}{"workflow_id": wf, "node_id": b}, &del)
	if del.RemovedConnections != 1 {
		t.Errorf("removed_connections = %d, want 1", del.RemovedConnections)
	}
	got = getWorkflow(t, s, wf)
	if len(got.Nodes) != 1 || len(got.Connections) != 0 {
		t.Fatalf("after delete: %d nodes, %d connections", len(got.Nodes), len(got.Connections))
	}
	wantErr(t, s, "workflow_node_delete", map[string]interface{}{"workflow_id": wf, "node_id": b}, "not found")
}

// Another profile's workflow is "not found" to every authoring tool.
func TestAuthoringToolsKeepToTheProfile(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.db.DB.Exec(`INSERT INTO workflows (id, name, profile_id, created_at, updated_at) VALUES ('wf-other', 'theirs', 'other', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]interface{}{
		"workflow_connect":     {"workflow_id": "wf-other", "from": "x", "to": "y"},
		"workflow_disconnect":  {"workflow_id": "wf-other", "connection_id": "c"},
		"workflow_node_update": {"workflow_id": "wf-other", "node_id": "n"},
		"workflow_node_delete": {"workflow_id": "wf-other", "node_id": "n"},
		"workflow_inputs":      {"id": "wf-other"},
		"workflow_executions":  {"workflow_id": "wf-other"},
		"workflow_save":        {"workflow": map[string]interface{}{"id": "wf-other", "name": "mine now"}},
	} {
		wantErr(t, s, name, args, `workflow "wf-other" not found`)
	}
}

func TestWorkflowSaveCreatesThenReplaces(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	doc := map[string]interface{}{
		"name": "saved",
		"nodes": []map[string]interface{}{
			{"id": "t", "node_type": "trigger.manual", "name": "Trigger", "config": map[string]interface{}{}},
			{"id": "s", "node_type": "core.set", "name": "Set", "config": map[string]interface{}{"assignments": `[{"field":"x","value":"1"}]`}},
		},
		"connections": []map[string]interface{}{
			{"id": "c1", "source_node_id": "t", "source_handle": "main", "target_node_id": "s", "target_handle": "main"},
		},
	}
	var saved savedWorkflow
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": doc}, &saved)
	if saved.ID == "" || saved.Name != "saved" || saved.Nodes != nil {
		t.Fatalf("save result = %+v, want the workflow without its nodes", saved)
	}
	got := getWorkflow(t, s, saved.ID)
	if len(got.Nodes) != 2 || len(got.Connections) != 1 {
		t.Fatalf("saved workflow: %d nodes, %d connections", len(got.Nodes), len(got.Connections))
	}

	doc["id"], doc["name"] = saved.ID, "renamed"
	doc["connections"] = []map[string]interface{}{}
	callInto(t, s, "workflow_save", map[string]interface{}{"workflow": doc}, nil)
	got = getWorkflow(t, s, saved.ID)
	if got.Name != "renamed" || len(got.Connections) != 0 {
		t.Fatalf("replaced workflow: %+v", got)
	}

	wantErr(t, s, "workflow_save", map[string]interface{}{}, "workflow is required")
	wantErr(t, s, "workflow_save", map[string]interface{}{"workflow": map[string]interface{}{"nodes": "not a list"}}, "parse workflow document")
}

func TestWorkflowTemplatesSearchAndInputs(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	var templates []struct {
		ID     string   `json:"id"`
		Inputs []string `json:"inputs"`
	}
	callInto(t, s, "workflow_template_list", map[string]interface{}{}, &templates)
	if len(templates) == 0 {
		t.Fatal("no bundled templates")
	}
	var show struct {
		ID         string                 `json:"id"`
		Definition map[string]interface{} `json:"definition"`
	}
	callInto(t, s, "workflow_template_show", map[string]interface{}{"id": "gemimg"}, &show)
	if show.ID != "gemimg" || show.Definition["nodes"] == nil {
		t.Fatalf("template_show = %+v", show)
	}
	wantErr(t, s, "workflow_template_show", map[string]interface{}{"id": "no-such"}, `unknown template "no-such"`)
	wantErr(t, s, "workflow_template_instantiate", map[string]interface{}{"id": "no-such"}, `unknown template "no-such"`)

	var wf savedWorkflow
	callInto(t, s, "workflow_template_instantiate", map[string]interface{}{"id": "gemimg"}, &wf)
	if wf.ID == "" || len(wf.Nodes) != 2 || len(wf.Connections) != 1 {
		t.Fatalf("instantiated = %+v", wf)
	}
	if got := getWorkflow(t, s, wf.ID); len(got.Nodes) != 2 {
		t.Fatalf("the instantiated workflow is not saved: %+v", got)
	}
	if want := workflow.SavedTemplateDescription("gemimg"); want == "" || wf.Description != want || strings.Contains(wf.Description, "templates run") {
		t.Errorf("instantiated description = %q, want the template's summary %q", wf.Description, want)
	}

	var inputs struct {
		Workflow string `json:"workflow"`
		Fields   []struct {
			Name string `json:"name"`
		} `json:"fields"`
		Skeleton map[string]interface{} `json:"skeleton"`
	}
	callInto(t, s, "workflow_inputs", map[string]interface{}{"id": wf.ID}, &inputs)
	if inputs.Workflow != wf.ID || len(inputs.Fields) != 1 || inputs.Fields[0].Name != "prompt" {
		t.Fatalf("workflow_inputs = %+v", inputs)
	}
	if _, ok := inputs.Skeleton["prompt"]; !ok {
		t.Errorf("skeleton = %v, want a prompt", inputs.Skeleton)
	}
	wantErr(t, s, "workflow_inputs", map[string]interface{}{"id": "nope"}, `workflow "nope" not found`)

	var hits []struct {
		Source string `json:"source"`
		ID     string `json:"id"`
	}
	callInto(t, s, "workflow_search", map[string]interface{}{"query": "GEMINI.GENERATE_IMAGE"}, &hits)
	foundSaved := false
	for _, h := range hits {
		foundSaved = foundSaved || (h.Source == "workflow" && h.ID == wf.ID)
	}
	if !foundSaved {
		t.Errorf("search by node type did not find the saved workflow: %+v", hits)
	}
	callInto(t, s, "workflow_search", map[string]interface{}{"query": "gemimg"}, &hits)
	if len(hits) == 0 || hits[0].Source != "template" {
		t.Errorf("search by template id: %+v", hits)
	}
	wantErr(t, s, "workflow_search", map[string]interface{}{"query": 7}, "invalid arguments")
}

func TestWorkflowExecutionsLists(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	wf, _, _ := newAuthoringWorkflow(t, s)
	rt, _ := s.runtime()
	if _, err := rt.db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, created_at) VALUES ('ex-1', ?, 'SUCCESS', 'manual', 'default', '2026-10-01T00:00:00Z')`, wf); err != nil {
		t.Fatal(err)
	}
	var one []struct {
		ID string `json:"id"`
	}
	callInto(t, s, "workflow_executions", map[string]interface{}{"workflow_id": wf}, &one)
	if len(one) != 1 || one[0].ID != "ex-1" {
		t.Fatalf("executions of the workflow: %+v", one)
	}
	var all []struct {
		ID           string `json:"id"`
		WorkflowName string `json:"workflow_name"`
	}
	callInto(t, s, "workflow_executions", map[string]interface{}{"limit": 5}, &all)
	if len(all) != 1 || all[0].WorkflowName != "wired" {
		t.Fatalf("executions of the profile: %+v", all)
	}
	wantErr(t, s, "workflow_executions", map[string]interface{}{"workflow_id": "nope"}, `workflow "nope" not found`)
}

// Every new mutating tool is refused without --allow-mutations and absent
// from tools/list; every new read-only one is served and annotated so.
func TestAuthoringToolsGateAndAnnotations(t *testing.T) {
	s := newTestServer(t)
	mutating := []string{"workflow_connect", "workflow_disconnect", "workflow_node_update", "workflow_node_delete", "workflow_save", "workflow_template_instantiate", "node_run"}
	readOnly := []string{"workflow_search", "workflow_inputs", "workflow_executions", "workflow_template_list", "workflow_template_show", "doctor"}
	names := toolsListNames(t, newTestServer(t))
	for _, name := range mutating {
		wantErr(t, s, name, map[string]interface{}{}, "--allow-mutations")
		if names[name] {
			t.Errorf("%s is listed without --allow-mutations", name)
		}
	}
	defs := map[string]map[string]bool{}
	for _, d := range toolDefinitions(true) {
		defs[d["name"].(string)], _ = d["annotations"].(map[string]bool)
	}
	for _, name := range readOnly {
		if !names[name] {
			t.Errorf("%s is not listed", name)
		}
		if !defs[name]["readOnlyHint"] {
			t.Errorf("%s is not annotated readOnly", name)
		}
	}
	for _, name := range mutating {
		a, ok := defs[name]
		if !ok || a["readOnlyHint"] {
			t.Errorf("%s annotations %v", name, a)
		}
	}
	for _, name := range []string{"workflow_disconnect", "workflow_node_delete", "workflow_save", "node_run"} {
		if !defs[name]["destructiveHint"] {
			t.Errorf("%s is not annotated destructive", name)
		}
	}
}
