package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

func persistTestEnv(t *testing.T) (home string, cfg *globalConfig) {
	t.Helper()
	// The tests act as the operator: an agent-marked environment would start new
	// workflows inactive.
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, &globalConfig{DBPath: filepath.Join(t.TempDir(), "wf.db"), JSONOutput: true, ProfileID: "default"}
}

func sqlNodeTypes(t *testing.T, dbPath, wfID string) map[string]string {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wf, err := workflow.NewSQLiteWorkflowStore(db.DB).GetWorkflow(context.Background(), wfID)
	if err != nil || wf == nil {
		t.Fatalf("SQLite has no workflow %s (%v)", wfID, err)
	}
	out := map[string]string{}
	for _, n := range wf.Nodes {
		out[n.ID] = n.Type
	}
	return out
}

// TestWorkflowCreatePersistsToSQLite: `workflow create` writes the SQLite
// row too, and a run of the built workflow reports every node's type.
func TestWorkflowCreatePersistsToSQLite(t *testing.T) {
	_, cfg := persistTestEnv(t)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "persist-wf")), &created); err != nil {
		t.Fatal(err)
	}
	sqlNodeTypes(t, cfg.DBPath, created.ID) // row exists right after create

	add := func(typ, name string, extra ...string) string {
		var n workflow.WorkflowNode
		args := append([]string{"node", "add", created.ID, "--type", typ, "--name", name}, extra...)
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, args...)), &n); err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	trig := add("trigger.manual", "Start")
	set := add("core.set", "Set", "--config", `{"assignments":[{"field":"ok","value":"yes"}]}`)
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", trig, "--to", set)
	runWorkflowSubcmd(t, cfg, "activate", created.ID)

	var run struct {
		Status string `json:"status"`
		Nodes  []struct {
			NodeID string `json:"node_id"`
			Type   string `json:"type"`
		} `json:"nodes"`
	}
	out := runWorkflowSubcmd(t, cfg, "run", created.ID, "--timeout", "30s")
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatalf("run --json: %v\n%s", err, out)
	}
	if len(run.Nodes) == 0 {
		t.Fatalf("no nodes in run output: %s", out)
	}
	for _, n := range run.Nodes {
		if n.Type == "" {
			t.Errorf("node %s has an empty type in run --json: %s", n.NodeID, out)
		}
	}
}

// TestWorkflowFileOnlyBackfill: a workflow saved only to the file store
// (the desktop editor's path) gets its SQLite rows on the next store open;
// the marker makes later opens a no-op until the file changes.
func TestWorkflowFileOnlyBackfill(t *testing.T) {
	home, cfg := persistTestEnv(t)
	dir := filepath.Join(home, ".monoagent", "workflows")
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	wf := &workflow.Workflow{ID: "file-only-wf", Name: "file only", ProfileID: "default", Version: 1, CreatedAt: now, UpdatedAt: now,
		Nodes: []workflow.WorkflowNode{
			{ID: "fo-trigger", WorkflowID: "file-only-wf", Type: "trigger.manual", Name: "Start", Config: map[string]any{}},
			{ID: "fo-set", WorkflowID: "file-only-wf", Type: "core.set", Name: "Set", Config: map[string]any{"fields": map[string]any{}}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "fo-c1", WorkflowID: "file-only-wf", SourceNodeID: "fo-trigger",
			SourceHandle: "main", TargetNodeID: "fo-set", TargetHandle: "main"}},
	}
	if err := files.SaveWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	synced, err := syncFileWorkflowsToSQL(context.Background(), db.DB, dir)
	if err != nil || len(synced) != 1 || synced[0] != "file-only-wf" {
		t.Fatalf("first sync = %v, %v", synced, err)
	}
	if types := sqlNodeTypes(t, cfg.DBPath, "file-only-wf"); types["fo-trigger"] != "trigger.manual" || types["fo-set"] != "core.set" {
		t.Errorf("backfilled types = %v", types)
	}
	if again, _ := syncFileWorkflowsToSQL(context.Background(), db.DB, dir); len(again) != 0 {
		t.Errorf("marker did not stop a re-sync: %v", again)
	}
	// An edit (new file mtime/size) is picked up once.
	wf.Nodes[1].Name = "Set renamed"
	time.Sleep(10 * time.Millisecond)
	if err := files.SaveWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	if again, _ := syncFileWorkflowsToSQL(context.Background(), db.DB, dir); len(again) != 1 {
		t.Errorf("changed file not re-synced: %v", again)
	}
	if _, err := os.Stat(filepath.Join(dir, syncMarkerName)); err == nil {
		t.Error("marker must not live inside the workflow directory")
	}

	// Through the CLI: `workflow list` opens the store, which backfills.
	wf2 := *wf
	wf2.ID, wf2.Name = "file-only-2", "second"
	wf2.Nodes = []workflow.WorkflowNode{{ID: "fo2-trigger", WorkflowID: "file-only-2", Type: "trigger.manual", Name: "Start", Config: map[string]any{}}}
	wf2.Connections = nil
	if err := files.SaveWorkflow(context.Background(), &wf2); err != nil {
		t.Fatal(err)
	}
	runWorkflowSubcmd(t, cfg, "list")
	// A file that appeared after the first use is not backfilled by a command: it waits to be
	// adopted by an operator (doctor), and is then created inactive.
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'file-only-2'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a file that appeared later was backfilled by list (%d rows, %v)", n, err)
	}
	adopter := workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db.DB))
	shown, _, err := adopter.AdoptableFiles(context.Background())
	if err != nil || len(shown) != 1 || shown[0].ID != "file-only-2" {
		t.Fatalf("adoptable: %+v %v", shown, err)
	}
	if err := adopter.AdoptFile(context.Background(), "file-only-2", shown[0].ProfileID, shown[0].SHA); err != nil {
		t.Fatalf("adopting: %v", err)
	}
	if types := sqlNodeTypes(t, cfg.DBPath, "file-only-2"); types["fo2-trigger"] != "trigger.manual" {
		t.Errorf("adopting did not create the workflow: %v", types)
	}
}

// TestWorkflowBackfillSkipsForeignIDs: a file workflow whose node ids
// belong to another workflow in SQLite is not synced (the upsert would
// move those nodes).
func TestWorkflowBackfillSkipsForeignIDs(t *testing.T) {
	home, cfg := persistTestEnv(t)
	path := writeTempWorkflow(t, validWorkflowFile) // node ids "trigger", "set"
	runWorkflowSubcmd(t, cfg, "import", "--file", path)
	dir := filepath.Join(home, ".monoagent", "workflows")
	files, _ := workflow.NewWorkflowFileStore(dir)
	now := time.Now().UTC()
	clash := &workflow.Workflow{ID: "clash-wf", Name: "clash", CreatedAt: now, UpdatedAt: now,
		Nodes: []workflow.WorkflowNode{{ID: "trigger", WorkflowID: "clash-wf", Type: "trigger.manual", Name: "T", Config: map[string]any{}}}}
	if err := files.SaveWorkflow(context.Background(), clash); err != nil {
		t.Fatal(err)
	}
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	synced, _ := syncFileWorkflowsToSQL(context.Background(), db.DB, dir)
	for _, id := range synced {
		if id == "clash-wf" {
			t.Fatal("a workflow sharing node ids with another was synced")
		}
	}
	var owner string
	_ = db.DB.QueryRow(`SELECT workflow_id FROM workflow_nodes WHERE id = 'trigger'`).Scan(&owner)
	if owner == "clash-wf" {
		t.Error("node moved to the clashing workflow")
	}
}

type importOut struct {
	ID                 string   `json:"id"`
	Status             string   `json:"status"`
	MissingAutomations []string `json:"missingAutomations"`
	Warnings           []string `json:"warnings"`
	InstallCommand     string   `json:"installCommand"`
	CopyOf             string   `json:"copyOf"`
	CopyReason         string   `json:"copyReason"`
}

func importJSON(t *testing.T, cfg *globalConfig, args ...string) importOut {
	t.Helper()
	var r importOut
	out := runWorkflowSubcmd(t, cfg, append([]string{"import"}, args...)...)
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("import output %q: %v", out, err)
	}
	return r
}

func countWorkflows(t *testing.T, cfg *globalConfig) int {
	t.Helper()
	var list []json.RawMessage
	out := runWorkflowSubcmd(t, cfg, "list")
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		var wrapped map[string]json.RawMessage
		if json.Unmarshal([]byte(out), &wrapped) == nil {
			for _, v := range wrapped {
				if json.Unmarshal(v, &list) == nil {
					break
				}
			}
		}
	}
	return len(list)
}

func TestWorkflowImportIdempotent(t *testing.T) {
	_, cfg := persistTestEnv(t)
	withID := strings.Replace(validWorkflowFile, `"name": "test-wf",`, `"id": "wf-fixed-id", "name": "test-wf",`, 1)
	path := writeTempWorkflow(t, withID)

	first := importJSON(t, cfg, "--file", path, "--yes")
	if first.Status != importCreated || first.ID != "wf-fixed-id" {
		t.Fatalf("first import = %+v", first)
	}
	again := importJSON(t, cfg, "--file", path, "--yes")
	if again.Status != importUnchanged || again.ID != first.ID {
		t.Errorf("re-import = %+v", again)
	}
	if n := countWorkflows(t, cfg); n != 1 {
		t.Errorf("workflows after re-import = %d, want 1", n)
	}

	// A changed file with the same id updates in place.
	changed := strings.Replace(withID, `"name": "Set"`, `"name": "Set v2"`, 1)
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	upd := importJSON(t, cfg, "--file", path)
	if upd.Status != importUpdated || upd.ID != first.ID {
		t.Errorf("changed re-import = %+v", upd)
	}

	// --as-new forces a copy.
	copyOf := importJSON(t, cfg, "--file", path, "--as-new")
	if copyOf.Status != importCreated || copyOf.ID == first.ID {
		t.Errorf("--as-new = %+v", copyOf)
	}
	if n := countWorkflows(t, cfg); n != 2 {
		t.Errorf("workflows after --as-new = %d, want 2", n)
	}
}

func TestWorkflowImportIdempotentWithoutID(t *testing.T) {
	_, cfg := persistTestEnv(t)
	path := writeTempWorkflow(t, validWorkflowFile) // no id in the file
	first := importJSON(t, cfg, "--file", path)
	if again := importJSON(t, cfg, "--file", path); again.Status != importUnchanged || again.ID != first.ID {
		t.Errorf("same-content re-import = %+v (first %+v)", again, first)
	}
	// Same source and name, new content: updated in place.
	if err := os.WriteFile(path, []byte(strings.Replace(validWorkflowFile, `"x": 1`, `"x": 5`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if upd := importJSON(t, cfg, "--file", path); upd.Status != importUpdated || upd.ID != first.ID {
		t.Errorf("same-name re-import = %+v", upd)
	}
	if n := countWorkflows(t, cfg); n != 1 {
		t.Errorf("workflows = %d, want 1", n)
	}
}

func TestWorkflowImportReportsMissingBundles(t *testing.T) {
	_, cfg := persistTestEnv(t)
	bundled := strings.Replace(validWorkflowFile, `"name": "test-wf",`,
		`"name": "test-wf", "automations": {"acme-missing": {"version": "1.0.0", "sha256": "00", "mpkg": ""}},`, 1)
	path := writeTempWorkflow(t, bundled)
	r := importJSON(t, cfg, "--file", path)
	if r.Status != importCreated || len(r.MissingAutomations) != 1 || r.MissingAutomations[0] != "acme-missing" ||
		!strings.Contains(r.InstallCommand, "workflow import --file "+path+" --yes") {
		t.Errorf("import = %+v", r)
	}
	human := &globalConfig{DBPath: cfg.DBPath, ProfileID: "default"}
	out := runWorkflowSubcmd(t, human, "import", "--file", path)
	if !strings.Contains(out, "not installed: acme-missing") || !strings.Contains(out, "Install them with: monoagentcli workflow import --file") ||
		!strings.Contains(out, "already imported and unchanged") {
		t.Errorf("human output:\n%s", out)
	}
}

// TestWorkflowImportMatchesUnindexed (D4): a workflow imported by v0.70 has
// no entry in workflow-imports.json. Re-importing the same file must find
// it by content (unchanged), a changed file with the same name and node
// types must update it, and the match is then recorded in the index.
func TestWorkflowImportMatchesUnindexed(t *testing.T) {
	home, cfg := persistTestEnv(t)
	path := writeTempWorkflow(t, validWorkflowFile) // no id: v0.70 gave it a fresh one
	first := importJSON(t, cfg, "--file", path)
	index := filepath.Join(home, ".monoagent", "workflow-imports.json")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}

	again := importJSON(t, cfg, "--file", path)
	if again.Status != importUnchanged || again.ID != first.ID {
		t.Fatalf("re-import without index = %+v, want unchanged %s", again, first.ID)
	}
	if n := countWorkflows(t, cfg); n != 1 {
		t.Errorf("workflows = %d, want 1", n)
	}
	if b, err := os.ReadFile(index); err != nil || !strings.Contains(string(b), first.ID) {
		t.Errorf("match not recorded in the index: %s %v", b, err)
	}

	// Same name and node types but different content, with no index: the
	// local workflow is never overwritten (D9); the file becomes a copy.
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	other := writeTempWorkflow(t, strings.Replace(validWorkflowFile, `"x": 1`, `"x": 7`, 1))
	cp := importJSON(t, cfg, "--file", other)
	if cp.Status != importCreated || cp.ID == first.ID || !strings.Contains(strings.Join(cp.Warnings, " "), first.ID) {
		t.Errorf("same name+types = %+v", cp)
	}

	// Same name but different node types is a different workflow.
	diff := writeTempWorkflow(t, strings.Replace(validWorkflowFile, `"type": "core.set"`, `"type": "core.if"`, 1))
	if created := importJSON(t, cfg, "--file", diff, "--as-new=false"); created.Status != importCreated || created.ID == first.ID {
		t.Errorf("different node types = %+v", created)
	}
}

func workflowNodeConfig(t *testing.T, cfg *globalConfig, wfID, nodeName string) map[string]any {
	t.Helper()
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wf, err := openWorkflowStore(db).GetWorkflow(context.Background(), wfID)
	if err != nil || wf == nil {
		t.Fatalf("workflow %s: %v", wfID, err)
	}
	for _, n := range wf.Nodes {
		if n.Name == nodeName {
			return n.Config
		}
	}
	t.Fatalf("no node %q in %s", nodeName, wfID)
	return nil
}

// TestWorkflowImportNeverOverwritesHandMade (D9): a workflow the user made
// by hand with the file's name and node types is left alone; the file is
// imported as a copy with a warning, and --replace is the explicit way.
func TestWorkflowImportNeverOverwritesHandMade(t *testing.T) {
	_, cfg := persistTestEnv(t)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "test-wf")), &created); err != nil {
		t.Fatal(err)
	}
	runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", "trigger.manual", "--name", "Manual")
	runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", "core.set", "--name", "Set",
		"--config", `{"assignments":[{"field":"mine","value":"hand-made"}]}`)

	r := importJSON(t, cfg, "--file", writeTempWorkflow(t, validWorkflowFile))
	if r.Status != importCreated || r.ID == created.ID || len(r.Warnings) != 1 ||
		!strings.Contains(r.Warnings[0], created.ID) || !strings.Contains(r.Warnings[0], "--replace") {
		t.Fatalf("import over a hand-made workflow = %+v", r)
	}
	if cfgMap := workflowNodeConfig(t, cfg, created.ID, "Set"); !strings.Contains(fmt.Sprint(cfgMap), "hand-made") {
		t.Errorf("hand-made config lost: %v", cfgMap)
	}

	// --replace <id> is the explicit overwrite.
	rep := importJSON(t, cfg, "--file", writeTempWorkflow(t, validWorkflowFile), "--replace", created.ID)
	if rep.Status != importUpdated || rep.ID != created.ID {
		t.Errorf("--replace = %+v", rep)
	}
}

// TestWorkflowImportKeepsLocalEdits (D9): re-importing a changed file does
// not overwrite a workflow the user edited after the previous import; a
// re-export of an unedited import still updates in place.
func TestWorkflowImportKeepsLocalEdits(t *testing.T) {
	_, cfg := persistTestEnv(t)
	path := writeTempWorkflow(t, validWorkflowFile)
	first := importJSON(t, cfg, "--file", path)

	// Unedited: a changed file updates in place.
	if err := os.WriteFile(path, []byte(strings.Replace(validWorkflowFile, `"x": 1`, `"x": 2`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if upd := importJSON(t, cfg, "--file", path); upd.Status != importUpdated || upd.ID != first.ID {
		t.Fatalf("unedited re-import = %+v", upd)
	}

	// The user edits the workflow (the desktop editor saves the whole thing).
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := openWorkflowStore(db)
	wf, _ := store.GetWorkflow(context.Background(), first.ID)
	for i := range wf.Nodes {
		if wf.Nodes[i].Name == "Set" {
			wf.Nodes[i].Config = map[string]any{"assignments": []any{map[string]any{"field": "edited", "value": "by-user"}}}
		}
	}
	if err := store.SaveWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := os.WriteFile(path, []byte(strings.Replace(validWorkflowFile, `"x": 1`, `"x": 3`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cp := importJSON(t, cfg, "--file", path)
	if cp.Status != importCreated || cp.ID == first.ID || len(cp.Warnings) == 0 || !strings.Contains(cp.Warnings[0], first.ID) {
		t.Fatalf("re-import over an edited workflow = %+v", cp)
	}
	if cfgMap := workflowNodeConfig(t, cfg, first.ID, "Set"); !strings.Contains(fmt.Sprint(cfgMap), "by-user") {
		t.Errorf("user edit lost: %v", cfgMap)
	}
}
