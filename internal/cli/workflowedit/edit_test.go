package workflowedit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

const editorDoc = `{"id":"wf-e","name":"from-editor","description":"d","is_active":true,
  "nodes":[
    {"id":"n1","node_type":"trigger.manual","name":"Start","config":{},"position_x":1.5,"position_y":2.5},
    {"id":"n2","node_type":"core.set","name":"Set","config":{"field":"x"}}],
  "connections":[{"id":"c1","source_node_id":"n1","source_handle":"main","target_node_id":"n2","target_handle":"main"}]}`

func TestParseNodeHandle(t *testing.T) {
	t.Parallel()
	for in, want := range map[string][2]string{"a": {"a", "main"}, "a:": {"a", "main"}, "a:true": {"a", "true"}, "a:b:c": {"a", "b:c"}} {
		id, h, err := parseNodeHandle(in)
		if err != nil || id != want[0] || h != want[1] {
			t.Errorf("parseNodeHandle(%q) = %q, %q, %v; want %v", in, id, h, err, want)
		}
	}
	if _, _, err := parseNodeHandle(""); err == nil {
		t.Error("an empty handle was accepted")
	}
}

// save creates the workflow (tagged with the profile, default schemas filled in), and an edit
// keeps the stored activation, version and creation time.
func TestSaveCreatesAndEdits(t *testing.T) {
	cfg := newTestDB(t, "")
	out, err := run(t, cfg, editorDoc, "save")
	var saved workflow.Workflow
	if err != nil || json.Unmarshal([]byte(out), &saved) != nil || saved.ID != "wf-e" || !saved.IsActive || len(saved.Nodes) != 0 {
		t.Fatalf("save = %q, %v", out, err)
	}
	got := load(t, cfg, "wf-e")
	if len(got.Nodes) != 2 || len(got.Connections) != 1 || got.Nodes[0].PositionX != 1.5 {
		t.Fatalf("saved %+v", got)
	}
	out, err = run(t, cfg, `{"id":"wf-e","name":"renamed","is_active":false}`, "save")
	var edited workflow.Workflow
	if err != nil || json.Unmarshal([]byte(out), &edited) != nil || edited.Name != "renamed" || !edited.IsActive || !edited.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("edit = %q, %v", out, err)
	}
	cfg.JSONOutput = false
	if out, err = run(t, cfg, editorDoc, "save"); err != nil || out != "Saved workflow: from-editor  (id: wf-e)\n" {
		t.Fatalf("save (text) = %q, %v", out, err)
	}
}

func TestSaveRejects(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('theirs','T',0,'other')`)
	for _, tc := range []struct {
		stdin string
		args  []string
		code  int
	}{
		{"not json", []string{"save"}, 3},
		{`{"id":"theirs","name":"stolen"}`, []string{"save"}, 2},
		{"", []string{"save", "--file", "/nonexistent/doc.json"}, 2},
	} {
		if err := execute(cfg, tc.stdin, tc.args...); exitCode(err) != tc.code {
			t.Errorf("%v: exit %d (%v), want %d", tc.args, exitCode(err), err, tc.code)
		}
	}
}

// node add/set/remove and connect/disconnect edit the stored definition; removing a node drops
// its connections.
func TestNodeAndConnectionEdits(t *testing.T) {
	cfg := newTestDB(t, "")
	if err := execute(cfg, editorDoc, "save"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, cfg, "", "node", "add", "wf-e", "--type", "core.set", "--name", "Third", "--config", `{"v":1}`, "--x", "5")
	var added workflow.WorkflowNode
	if err != nil || json.Unmarshal([]byte(out), &added) != nil || added.Name != "Third" || added.PositionX != 5 || added.Config["v"] != float64(1) {
		t.Fatalf("node add = %q, %v", out, err)
	}
	out, err = run(t, cfg, "", "connect", "wf-e", "--from", "n2", "--to", added.ID+":main")
	var conn workflow.WorkflowConnection
	if err != nil || json.Unmarshal([]byte(out), &conn) != nil || conn.SourceHandle != "main" || conn.Position != 1 {
		t.Fatalf("connect = %q, %v", out, err)
	}
	out, err = run(t, cfg, "", "node", "set", "wf-e", "n2", "--name", "Set2", "--y", "7")
	if err != nil || !strings.Contains(out, `"name":"Set2"`) || !strings.Contains(out, `"position_y":7`) {
		t.Fatalf("node set = %q, %v", out, err)
	}
	if wf := load(t, cfg, "wf-e"); len(wf.Nodes) != 3 || len(wf.Connections) != 2 {
		t.Fatalf("after add/connect/set: %d nodes, %d connections", len(wf.Nodes), len(wf.Connections))
	}
	out, err = run(t, cfg, "", "node", "list", "wf-e")
	if err != nil || strings.Count(out, `"node_type"`) != 3 {
		t.Fatalf("node list = %q, %v", out, err)
	}
	if out, err = run(t, cfg, "", "node", "remove", "wf-e", "n2"); err != nil || !strings.Contains(out, `"id":"n2"`) {
		t.Fatalf("node remove = %q, %v", out, err)
	}
	if wf := load(t, cfg, "wf-e"); len(wf.Nodes) != 2 || len(wf.Connections) != 0 {
		t.Fatalf("after remove: %d nodes, %d connections", len(wf.Nodes), len(wf.Connections))
	}
	out, err = run(t, cfg, "", "connect", "wf-e", "--from", "n1", "--to", added.ID)
	if err != nil || json.Unmarshal([]byte(out), &conn) != nil {
		t.Fatalf("connect = %q, %v", out, err)
	}
	if out, err = run(t, cfg, "", "disconnect", "wf-e", conn.ID); err != nil || out != "Connection "+conn.ID+" removed.\n" {
		t.Fatalf("disconnect = %q, %v", out, err)
	}
}

func TestEditErrors(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, "")
	if err := execute(cfg, editorDoc, "save"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"node", "add", "nope", "--type", "core.set", "--name", "x"}, 2},
		{[]string{"node", "list", "nope"}, 2},
		{[]string{"node", "set", "nope", "n1"}, 2},
		{[]string{"node", "set", "wf-e", "nope"}, 2},
		{[]string{"node", "set", "wf-e", "n1", "--config", "{"}, 3},
		{[]string{"node", "remove", "wf-e", "nope"}, 2},
		{[]string{"connect", "nope", "--from", "a", "--to", "b"}, 2},
		{[]string{"disconnect", "nope", "c1"}, 2},
		// Not one of the CLI's exit-code errors: exit 1, as before.
		{[]string{"node", "add", "wf-e", "--type", "core.set", "--name", "x", "--config", "{"}, 1},
		{[]string{"disconnect", "wf-e", "nope"}, 1},
	} {
		if err := execute(cfg, "", tc.args...); exitCode(err) != tc.code {
			t.Errorf("%v: exit %d (%v), want %d", tc.args, exitCode(err), err, tc.code)
		}
	}
}
