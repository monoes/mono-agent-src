package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runWorkflowCmdIn runs `workflow <args>` with stdin, returning stdout and
// the command's error (runWorkflowSubcmd fails the test on any error).
func runWorkflowCmdIn(t *testing.T, cfg *globalConfig, stdin string, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		c := newWorkflowCmd(cfg)
		c.SetArgs(args)
		c.SetIn(strings.NewReader(stdin))
		err = c.Execute()
	})
	return out, err
}

// otherProfileCfg adds the profile "other" to cfg's database and returns a
// config acting as it.
func otherProfileCfg(t *testing.T, cfg *globalConfig) *globalConfig {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name, created_at) VALUES ('other', 'Other', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	return &globalConfig{DBPath: cfg.DBPath, JSONOutput: true, ProfileID: "other"}
}

func saveDoc(t *testing.T, cfg *globalConfig, doc string) workflow.Workflow {
	t.Helper()
	out, err := runWorkflowCmdIn(t, cfg, doc, "save")
	if err != nil {
		t.Fatalf("workflow save: %v", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal([]byte(out), &wf); err != nil {
		t.Fatalf("save output %q: %v", out, err)
	}
	return wf
}

const editorDoc = `{"name":"from-editor","description":"d","is_active":true,
  "nodes":[
    {"id":"ed-n1","node_type":"trigger.manual","name":"Start","config":{},"position_x":1.5,"position_y":2.5},
    {"id":"ed-n2","node_type":"core.set","name":"Set","config":{"field":"x"},"position_x":10,"position_y":20,"disabled":true}],
  "connections":[{"id":"ed-c1","source_node_id":"ed-n1","source_handle":"main","target_node_id":"ed-n2","target_handle":"main"}]}`

// `workflow save` creates a workflow from the editor's document: the file
// under HOME/.monoagent/workflows, a SQLite row tagged with the profile,
// positions and connections intact, and default schemas filled in.
func TestWorkflowSaveCreates(t *testing.T) {
	home, cfg := persistTestEnv(t)
	saved := saveDoc(t, cfg, editorDoc)
	if saved.ID == "" || saved.Name != "from-editor" || !saved.IsActive || saved.Version != 1 || len(saved.Nodes) != 0 {
		t.Fatalf("save summary = %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "workflows", saved.ID+".json")); err != nil {
		t.Fatalf("no workflow file: %v", err)
	}
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.DB.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, saved.ID).Scan(&owner); err != nil || owner != "default" {
		t.Fatalf("SQLite row profile = %q, %v", owner, err)
	}
	db.Close()

	var got workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "get", saved.ID)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 2 || len(got.Connections) != 1 {
		t.Fatalf("get = %+v", got)
	}
	n1, n2 := got.Nodes[0], got.Nodes[1]
	if n1.ID != "ed-n1" || n1.Type != "trigger.manual" || n1.PositionX != 1.5 || n1.PositionY != 2.5 || n1.Schema == nil {
		t.Errorf("node 1 = %+v", n1)
	}
	if n2.Config["field"] != "x" || !n2.Disabled || n2.PositionX != 10 {
		t.Errorf("node 2 = %+v", n2)
	}
	if c := got.Connections[0]; c.SourceNodeID != "ed-n1" || c.TargetNodeID != "ed-n2" || c.SourceHandle != "main" {
		t.Errorf("connection = %+v", c)
	}
}

// Saving an edit replaces the definition but never the activation, the
// version or the creation time: the editor sends none of them.
func TestWorkflowSaveEditKeepsActivation(t *testing.T) {
	_, cfg := persistTestEnv(t)
	saved := saveDoc(t, cfg, editorDoc)

	edited := saveDoc(t, cfg, `{"id":"`+saved.ID+`","name":"renamed","nodes":[
		{"id":"ed-n1","node_type":"trigger.manual","name":"Start","config":{}}]}`)
	if !edited.IsActive || edited.Name != "renamed" || !edited.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("edit = %+v (created %v)", edited, saved.CreatedAt)
	}
	var got workflow.Workflow
	_ = json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "get", saved.ID)), &got)
	if len(got.Nodes) != 1 || len(got.Connections) != 0 || !got.IsActive {
		t.Fatalf("after edit = %+v", got)
	}

	runWorkflowSubcmd(t, cfg, "deactivate", saved.ID)
	if again := saveDoc(t, cfg, `{"id":"`+saved.ID+`","name":"renamed","is_active":true}`); again.IsActive {
		t.Fatal("saving an edit activated a deactivated workflow")
	}
}

func TestWorkflowSaveRejects(t *testing.T) {
	_, cfg := persistTestEnv(t)
	saved := saveDoc(t, cfg, editorDoc)

	if _, err := runWorkflowCmdIn(t, cfg, "not json", "save"); exitCode(err) != 3 {
		t.Errorf("bad JSON: exit %d, want 3", exitCode(err))
	}
	other := otherProfileCfg(t, cfg)
	if _, err := runWorkflowCmdIn(t, other, `{"id":"`+saved.ID+`","name":"stolen"}`, "save"); exitCode(err) != 2 {
		t.Errorf("another profile's id: exit %d, want 2", exitCode(err))
	}
	var got workflow.Workflow
	_ = json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "get", saved.ID)), &got)
	if got.Name != "from-editor" {
		t.Errorf("another profile overwrote the workflow: %+v", got)
	}
}

// `workflow list` shows only the active profile's workflows (a file with no
// profile counts as "default"), with node counts, and [] when empty.
func TestWorkflowListProfileAndNodeCount(t *testing.T) {
	home, cfg := persistTestEnv(t)
	other := otherProfileCfg(t, cfg) // the profile exists before the files are taken in
	// Files older versions left (they exist before the workflow store is first used).
	files, err := workflow.NewWorkflowFileStore(filepath.Join(home, ".monoagent", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range []*workflow.Workflow{
		{ID: "unowned", Name: "no profile", Nodes: []workflow.WorkflowNode{{ID: "u1", Type: "trigger.manual"}}},
		{ID: "theirs", Name: "other profile", ProfileID: "other"},
	} {
		if err := files.SaveWorkflow(context.Background(), wf); err != nil {
			t.Fatal(err)
		}
	}
	saved := saveDoc(t, cfg, editorDoc)

	var rows []struct {
		ID        string `json:"id"`
		NodeCount int    `json:"node_count"`
	}
	out := runWorkflowSubcmd(t, cfg, "list")
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.ID] = r.NodeCount
	}
	if len(rows) != 2 || counts[saved.ID] != 2 || counts["unowned"] != 1 {
		t.Fatalf("list = %s", out)
	}
	out = runWorkflowSubcmd(t, other, "list")
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].ID != "theirs" {
		t.Fatalf("other profile's list = %s", out)
	}
}

// get, export and delete treat another profile's workflow as not found, and
// `delete --yes` deletes without a prompt.
func TestWorkflowProfileScopedLookups(t *testing.T) {
	_, cfg := persistTestEnv(t)
	saved := saveDoc(t, cfg, editorDoc)
	other := otherProfileCfg(t, cfg)
	for _, args := range [][]string{{"get", saved.ID}, {"export", saved.ID}, {"delete", saved.ID, "--yes"}} {
		if _, err := runWorkflowCmdIn(t, other, "", args...); exitCode(err) != 2 {
			t.Errorf("%v from another profile: exit %d, want 2", args, exitCode(err))
		}
	}

	out, err := runWorkflowCmdIn(t, cfg, "", "delete", saved.ID, "--yes")
	if err != nil || !strings.Contains(out, `"deleted":true`) {
		t.Fatalf("delete --yes = %q, %v", out, err)
	}
	if _, err := runWorkflowCmdIn(t, cfg, "", "get", saved.ID); exitCode(err) != 2 {
		t.Errorf("get after delete: exit %d, want 2", exitCode(err))
	}
}
