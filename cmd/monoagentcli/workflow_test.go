package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// validWorkflowFile is a minimal, activation-valid workflow: a manual trigger
// feeding one core.set node.
const validWorkflowFile = `{
  "name": "test-wf",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "set", "type": "core.set", "name": "Set", "position": {"x": 1, "y": 0}, "config": {"assignments": [{"field": "a", "value": "1"}]}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"}
  ]
}`

// cycleWorkflowFile: trigger → a → b → a (cycle).
const cycleWorkflowFile = `{
  "name": "cycle-wf",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "a", "type": "core.set", "name": "A", "position": {"x": 1, "y": 0}, "config": {}},
    {"id": "b", "type": "core.set", "name": "B", "position": {"x": 2, "y": 0}, "config": {}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "a", "target_handle": "main"},
    {"id": "c2", "source": "a", "source_handle": "main", "target": "b", "target_handle": "main"},
    {"id": "c3", "source": "b", "source_handle": "main", "target": "a", "target_handle": "main"}
  ]
}`

// noTriggerWorkflowFile: valid structure but no trigger.* node.
const noTriggerWorkflowFile = `{
  "name": "no-trigger-wf",
  "nodes": [
    {"id": "set", "type": "core.set", "name": "Set", "position": {"x": 0, "y": 0}, "config": {}}
  ],
  "connections": []
}`

// handlelessWorkflowFile: connections without source_handle/target_handle,
// like the README flagship example — import accepts them and the engine
// routes on the "main" port, so validate must accept them too.
const handlelessWorkflowFile = `{
  "name": "handleless-wf",
  "nodes": [
    {"id": "trigger", "type": "trigger.schedule", "name": "Cron", "position": {"x": 0, "y": 0}, "config": {"cron": "0 0 9 * * *"}},
    {"id": "a", "type": "core.set", "name": "A", "position": {"x": 1, "y": 0}, "config": {"assignments": [{"field": "a", "value": "1"}]}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "target": "a"}
  ]
}`

// legacyHandlelessWorkflowFile: legacy export keys AND no connection
// handles — validate must normalize keys (like import) before checking.
const legacyHandlelessWorkflowFile = `{
  "name": "legacy-handleless-wf",
  "nodes": [
    {"id": "trigger", "node_type": "trigger.manual", "name": "Manual", "position_x": 0, "position_y": 0, "config": {}},
    {"id": "a", "node_type": "core.set", "name": "A", "position_x": 1, "position_y": 0, "config": {"assignments": [{"field": "a", "value": "1"}]}}
  ],
  "connections": [
    {"id": "c1", "source_node_id": "trigger", "target_node_id": "a"}
  ]
}`

func writeTempWorkflow(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp workflow: %v", err)
	}
	return path
}

func runValidateCmd(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "unused.db"), JSONOutput: jsonOut}
	cmd := newWorkflowValidateCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestWorkflowValidateFile_Valid(t *testing.T) {
	path := writeTempWorkflow(t, validWorkflowFile)
	out, err := runValidateCmd(t, false, "--file", path)
	if err != nil {
		t.Fatalf("expected valid workflow, got error: %v", err)
	}
	if !strings.Contains(out, "Workflow is valid.") {
		t.Fatalf("expected success message, got: %q", out)
	}
}

func TestWorkflowValidateFile_ValidJSONShape(t *testing.T) {
	path := writeTempWorkflow(t, validWorkflowFile)
	out, err := runValidateCmd(t, true, "--file", path)
	if err != nil {
		t.Fatalf("expected valid workflow, got error: %v", err)
	}
	var got validationJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (out: %q)", err, out)
	}
	if !got.Valid {
		t.Fatalf("expected valid=true, got %+v", got)
	}
}

func TestWorkflowValidateFile_Cycle(t *testing.T) {
	path := writeTempWorkflow(t, cycleWorkflowFile)
	out, err := runValidateCmd(t, false, "--file", path)
	if err == nil {
		t.Fatalf("expected cycle to be rejected, got success (out: %q)", out)
	}
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("expected exit code 3 for cycle, got %d (%v)", code, err)
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestWorkflowValidateFile_MissingTrigger(t *testing.T) {
	path := writeTempWorkflow(t, noTriggerWorkflowFile)
	_, err := runValidateCmd(t, false, "--file", path)
	if err == nil {
		t.Fatal("expected missing trigger to be rejected")
	}
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("expected exit code 3 for missing trigger, got %d (%v)", code, err)
	}
	if !strings.Contains(err.Error(), "trigger") {
		t.Fatalf("expected trigger error, got: %v", err)
	}
}

func TestWorkflowValidateFile_MissingFileIsNotFound(t *testing.T) {
	_, err := runValidateCmd(t, false, "--file", filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Fatalf("expected exit code 2 for missing file, got %d (%v)", code, err)
	}
}

func TestWorkflowValidateFile_InvalidJSONIsInvalidInput(t *testing.T) {
	path := writeTempWorkflow(t, "{not json")
	_, err := runValidateCmd(t, false, "--file", path)
	if err == nil {
		t.Fatal("expected error for unparseable JSON")
	}
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("expected exit code 3 for invalid JSON, got %d (%v)", code, err)
	}
}

func TestWorkflowValidateFile_HandlelessConnectionsDefaultToMain(t *testing.T) {
	path := writeTempWorkflow(t, handlelessWorkflowFile)
	out, err := runValidateCmd(t, false, "--file", path)
	if err != nil {
		t.Fatalf("expected handle-less connections to validate (handles default to main), got error: %v", err)
	}
	if !strings.Contains(out, "Workflow is valid.") {
		t.Fatalf("expected success message, got: %q", out)
	}
}

func TestWorkflowValidateFile_LegacyKeysNormalizedLikeImport(t *testing.T) {
	path := writeTempWorkflow(t, legacyHandlelessWorkflowFile)
	out, err := runValidateCmd(t, false, "--file", path)
	if err != nil {
		t.Fatalf("expected legacy-key workflow to validate via normalization, got error: %v", err)
	}
	if !strings.Contains(out, "Workflow is valid.") {
		t.Fatalf("expected success message, got: %q", out)
	}
}

func runNodeSchemaCmd(t *testing.T, nodeType string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "unused.db")}
	cmd := newNodeCmd(cfg)
	cmd.SetArgs([]string{"schema", nodeType})
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestNodeSchemaCmd_KnownType(t *testing.T) {
	out, err := runNodeSchemaCmd(t, "core.if")
	if err != nil {
		t.Fatalf("node schema core.if: %v", err)
	}
	var schema map[string]interface{}
	if err := json.Unmarshal([]byte(out), &schema); err != nil {
		t.Fatalf("schema output is not valid JSON: %v (out: %q)", err, out)
	}
	fields, ok := schema["fields"].([]interface{})
	if !ok || len(fields) == 0 {
		t.Fatalf("expected non-empty fields array, got: %q", out)
	}
	first, _ := fields[0].(map[string]interface{})
	if first["key"] != "condition" {
		t.Fatalf("expected first field key %q, got %v", "condition", first["key"])
	}
}

func TestNodeSchemaCmd_UnknownTypeExit2(t *testing.T) {
	_, err := runNodeSchemaCmd(t, "bogus.does_not_exist")
	if err == nil {
		t.Fatal("expected error for unknown node type")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Fatalf("expected exit code 2 for unknown node type, got %d (%v)", code, err)
	}
}

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"notfound sentinel", errNotFound("thing %q missing", "x"), 2},
		{"wrapped invalid input", fmt.Errorf("context: %w", ErrInvalidInput), 3},
		{"auth sentinel", errAuthConnection("boom"), 4},
		{"engine workflow not found", fmt.Errorf("trigger: %w", workflow.ErrWorkflowNotFound), 2},
		{"engine execution not found", fmt.Errorf("poll: %w", workflow.ErrExecutionNotFound), 2},
		{"engine no trigger", fmt.Errorf("activate: %w", workflow.ErrNoTriggerNode), 3},
		{"engine cycle", fmt.Errorf("build: %w", workflow.ErrCycleDetected), 3},
		{"plain error", errors.New("anything else"), 1},
	}
	for _, tc := range cases {
		if got := exitCodeFor(tc.err); got != tc.want {
			t.Errorf("%s: exitCodeFor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// captureStdout is defined in people_status_test.go and reused below.

// legacyWorkflowFile is what `workflow export` used to emit: a marshaled
// *workflow.Workflow with "node_type", "position_x"/"position_y" on nodes
// and "source_node_id"/"target_node_id" on connections.
const legacyWorkflowFile = `{
  "id": "legacy-wf",
  "name": "legacy-wf",
  "nodes": [
    {"id": "trigger", "node_type": "trigger.manual", "name": "Manual", "config": {}, "position_x": 5, "position_y": 7},
    {"id": "set", "node_type": "core.set", "name": "Set", "config": {"fields": {}}, "position_x": 9, "position_y": 11}
  ],
  "connections": [
    {"id": "c1", "source_node_id": "trigger", "source_handle": "main", "target_node_id": "set", "target_handle": "main"}
  ]
}`

// runWorkflowSubcmd executes a `workflow …` subcommand with JSON output on
// and returns everything it printed to stdout.
func runWorkflowSubcmd(t *testing.T, cfg *globalConfig, args ...string) string {
	t.Helper()
	cmd := newWorkflowCmd(cfg)
	cmd.SetArgs(args)
	return captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("workflow %v: %v", args, err)
		}
	})
}

// TestWorkflowExportImportRoundtrip guards RV6-3: `workflow export` must
// emit the documented WorkflowFile shape (type/source/target keys) that
// import parses natively, so export → wipe → import roundtrips nodes and
// connections losslessly. Export used to emit legacy-shape JSON that import
// silently parsed into empty node types and dangling connections.
func TestWorkflowExportImportRoundtrip(t *testing.T) {
	// Isolate the hybrid store's file half (~/.monoagent/workflows).
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(t.TempDir(), "roundtrip.db")
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}

	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "roundtrip-wf", "--description", "roundtrip")), &created); err != nil {
		t.Fatalf("parse create output: %v", err)
	}
	wfID := created.ID

	addNode := func(nodeType, name string, x, y float64) string {
		t.Helper()
		out := runWorkflowSubcmd(t, cfg, "node", "add", wfID,
			"--type", nodeType, "--name", name,
			"--x", fmt.Sprintf("%v", x), "--y", fmt.Sprintf("%v", y))
		var node workflow.WorkflowNode
		if err := json.Unmarshal([]byte(out), &node); err != nil {
			t.Fatalf("parse node add output: %v (out: %q)", err, out)
		}
		return node.ID
	}
	n1 := addNode("trigger.manual", "Trigger", 0, 0)
	n2 := addNode("core.set", "Set", 100, 200)
	n3 := addNode("core.set", "Set2", 300, 400)
	runWorkflowSubcmd(t, cfg, "connect", wfID, "--from", n1, "--to", n2)
	runWorkflowSubcmd(t, cfg, "connect", wfID, "--from", n2, "--to", n3)

	// Export must emit the native WorkflowFile shape, not legacy keys.
	exported := runWorkflowSubcmd(t, cfg, "export", wfID)
	var exportedFile workflow.WorkflowFile
	if err := json.Unmarshal([]byte(exported), &exportedFile); err != nil {
		t.Fatalf("parse export output: %v", err)
	}
	if len(exportedFile.Nodes) != 3 || len(exportedFile.Connections) != 2 {
		t.Fatalf("expected 3 nodes / 2 connections in export, got %d/%d", len(exportedFile.Nodes), len(exportedFile.Connections))
	}
	for _, n := range exportedFile.Nodes {
		if n.Type == "" {
			t.Fatalf("exported node %q has an empty type — legacy shape leaked", n.ID)
		}
	}
	for _, key := range []string{"node_type", "position_x", "source_node_id", "target_node_id"} {
		if strings.Contains(exported, `"`+key+`"`) {
			t.Fatalf("export must not emit legacy key %q", key)
		}
	}

	// Wipe both halves of the hybrid store, then import the export.
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, stmt := range []string{
		`DELETE FROM workflow_connections WHERE workflow_id = ?`,
		`DELETE FROM workflow_nodes WHERE workflow_id = ?`,
		`DELETE FROM workflows WHERE id = ?`,
	} {
		if _, err := db.DB.Exec(stmt, wfID); err != nil {
			t.Fatalf("wipe (%s): %v", stmt, err)
		}
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".monoagent", "workflows")); err != nil {
		t.Fatalf("wipe file store: %v", err)
	}

	exportPath := filepath.Join(t.TempDir(), "exported.json")
	if err := os.WriteFile(exportPath, []byte(exported), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	var imported struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "import", "--file", exportPath)), &imported); err != nil {
		t.Fatalf("parse import output: %v", err)
	}

	// Assert identical nodes via store queries.
	db2, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db2.Close()

	type nodeRow struct {
		id, typ string
		x, y    float64
	}
	got := map[string]nodeRow{}
	rows, err := db2.DB.Query(`SELECT id, node_type, position_x, position_y FROM workflow_nodes WHERE workflow_id = ?`, imported.ID)
	if err != nil {
		t.Fatalf("query nodes: %v", err)
	}
	for rows.Next() {
		var r nodeRow
		if err := rows.Scan(&r.id, &r.typ, &r.x, &r.y); err != nil {
			t.Fatalf("scan node: %v", err)
		}
		got[r.id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate nodes: %v", err)
	}
	rows.Close()

	wantNodes := map[string]nodeRow{
		n1: {n1, "trigger.manual", 0, 0},
		n2: {n2, "core.set", 100, 200},
		n3: {n3, "core.set", 300, 400},
	}
	if len(got) != len(wantNodes) {
		t.Fatalf("expected %d nodes after import, got %d (%v)", len(wantNodes), len(got), got)
	}
	for id, want := range wantNodes {
		if got[id] != want {
			t.Fatalf("node %q: got %+v, want %+v", id, got[id], want)
		}
	}

	// Assert identical connections — both the endpoint set and that every
	// edge's FKs resolve to nodes of this workflow (the join that used to
	// find zero rows when legacy imports produced empty endpoints).
	edges := map[string]string{}
	rows, err = db2.DB.Query(`SELECT c.source_node_id, c.target_node_id FROM workflow_connections c WHERE c.workflow_id = ?`, imported.ID)
	if err != nil {
		t.Fatalf("query connections: %v", err)
	}
	for rows.Next() {
		var s, g string
		if err := rows.Scan(&s, &g); err != nil {
			t.Fatalf("scan connection: %v", err)
		}
		edges[s] = g
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate connections: %v", err)
	}
	rows.Close()

	wantEdges := map[string]string{n1: n2, n2: n3}
	if len(edges) != len(wantEdges) {
		t.Fatalf("expected %d connections after import, got %d (%v)", len(wantEdges), len(edges), edges)
	}
	for s, wantTarget := range wantEdges {
		if edges[s] != wantTarget {
			t.Fatalf("edge from %s: got target %q, want %q", s, edges[s], wantTarget)
		}
	}

	var fkEdges int
	if err := db2.DB.QueryRow(`SELECT COUNT(*) FROM workflow_connections c
		JOIN workflow_nodes s ON s.id = c.source_node_id AND s.workflow_id = c.workflow_id
		JOIN workflow_nodes g ON g.id = c.target_node_id AND g.workflow_id = c.workflow_id
		WHERE c.workflow_id = ?`, imported.ID).Scan(&fkEdges); err != nil {
		t.Fatalf("count FK-resolvable edges: %v", err)
	}
	if fkEdges != 2 {
		t.Fatalf("expected 2 FK-resolvable edges after import, got %d", fkEdges)
	}
}

// TestWorkflowImportLegacyFormat guards RV6-3's import direction: legacy
// exports (node_type/position_x/source_node_id keys) must import with the
// correct node types, positions, and edges instead of silently producing
// empty node types and FK-dangling connections.
func TestWorkflowImportLegacyFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}

	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(legacyWorkflowFile), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	var imported struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "import", "--file", path)), &imported); err != nil {
		t.Fatalf("parse import output: %v", err)
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	gotTypes := map[string]string{}
	gotPos := map[string][2]float64{}
	rows, err := db.DB.Query(`SELECT id, node_type, position_x, position_y FROM workflow_nodes WHERE workflow_id = ?`, imported.ID)
	if err != nil {
		t.Fatalf("query nodes: %v", err)
	}
	for rows.Next() {
		var id, typ string
		var x, y float64
		if err := rows.Scan(&id, &typ, &x, &y); err != nil {
			t.Fatalf("scan node: %v", err)
		}
		gotTypes[id] = typ
		gotPos[id] = [2]float64{x, y}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate nodes: %v", err)
	}
	rows.Close()

	if gotTypes["trigger"] != "trigger.manual" || gotTypes["set"] != "core.set" {
		t.Fatalf("legacy node types not converted, got %v", gotTypes)
	}
	if gotPos["trigger"] != [2]float64{5, 7} || gotPos["set"] != [2]float64{9, 11} {
		t.Fatalf("legacy positions not converted, got %v", gotPos)
	}

	var src, dst string
	if err := db.DB.QueryRow(`SELECT c.source_node_id, c.target_node_id FROM workflow_connections c WHERE c.workflow_id = ?`, imported.ID).Scan(&src, &dst); err != nil {
		t.Fatalf("query connection: %v", err)
	}
	if src != "trigger" || dst != "set" {
		t.Fatalf("legacy edge not converted, got %s → %s", src, dst)
	}

	var fkEdges int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_connections c
		JOIN workflow_nodes s ON s.id = c.source_node_id AND s.workflow_id = c.workflow_id
		JOIN workflow_nodes g ON g.id = c.target_node_id AND g.workflow_id = c.workflow_id
		WHERE c.workflow_id = ?`, imported.ID).Scan(&fkEdges); err != nil {
		t.Fatalf("count FK-resolvable edges: %v", err)
	}
	if fkEdges != 1 {
		t.Fatalf("expected 1 FK-resolvable edge, got %d", fkEdges)
	}
}

// TestWorkflowImportRejectsEmptyNodeType: a node without any type key must
// be rejected with exit 3 and never persisted.
func TestWorkflowImportRejectsEmptyNodeType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "empty-type.db")
	// The rejected import bails before initDB, so the DB must exist already
	// for the not-persisted assertion below to query it.
	seedDB, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := seedDB.ApplyMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	cfg := &globalConfig{DBPath: dbPath, ProfileID: "default"}

	path := filepath.Join(t.TempDir(), "notype.json")
	if err := os.WriteFile(path, []byte(`{"name":"bad","nodes":[{"id":"n1","name":"N1","config":{}}],"connections":[]}`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	cmd := newWorkflowImportCmd(cfg)
	cmd.SetArgs([]string{"--file", path})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected import of a typeless node to be rejected")
	}
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("expected exit code 3 for typeless node, got %d (%v)", code, err)
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE name = 'bad'`).Scan(&n); err != nil {
		t.Fatalf("count workflows: %v", err)
	}
	if n != 0 {
		t.Fatalf("rejected workflow must not be persisted, found %d rows", n)
	}
}

// TestWorkflowImportFileErrorClassification guards RV1-6 for import: a
// missing --file is not-found (exit 2); any other IO error is a plain
// error (exit 1).
func TestWorkflowImportFileErrorClassification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "io.db"), ProfileID: "default"}

	cmd := newWorkflowImportCmd(cfg)
	cmd.SetArgs([]string{"--file", filepath.Join(t.TempDir(), "missing.json")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for missing --file")
	} else if code := exitCodeFor(err); code != 2 {
		t.Fatalf("missing --file: exit %d, want 2 (%v)", code, err)
	}

	// Reading a directory is an IO error that is not not-exist.
	cmd2 := newWorkflowImportCmd(cfg)
	cmd2.SetArgs([]string{"--file", t.TempDir()})
	if err := cmd2.Execute(); err == nil {
		t.Fatal("expected error for --file pointing at a directory")
	} else if code := exitCodeFor(err); code != 1 {
		t.Fatalf("directory --file: exit %d, want 1 (%v)", code, err)
	}
}

// TestWorkflowDeleteNonexistentIsNotFound guards RV4-4: deleting an unknown
// workflow must exit 2 (and must not spin up the engine to find that out).
func TestWorkflowDeleteNonexistentIsNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "delete.db"), ProfileID: "default"}

	cmd := newWorkflowDeleteCmd(cfg)
	cmd.SetArgs([]string{"no-such-workflow", "--force"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for deleting a nonexistent workflow")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Fatalf("expected exit code 2, got %d (%v)", code, err)
	}
}

// TestWorkflowRunNoWaitPersistsUnownedQueued guards the --no-wait fix
// (V3-F2): the command must persist the execution as an unowned QUEUED row
// (pid 0) for a live engine to adopt — NOT enqueue it into its own
// short-lived engine, where the run died cancelled the moment the CLI exited.
func TestWorkflowRunNoWaitPersistsUnownedQueued(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "nowait.db")
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}

	path := writeTempWorkflow(t, validWorkflowFile)
	var imported struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "import", "--file", path)), &imported); err != nil {
		t.Fatalf("parse import output: %v", err)
	}
	runWorkflowSubcmd(t, cfg, "activate", imported.ID)

	var noWait struct {
		ExecutionID string `json:"execution_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "run", imported.ID, "--no-wait")), &noWait); err != nil {
		t.Fatalf("parse run --no-wait output: %v", err)
	}
	if noWait.Status != "QUEUED" {
		t.Errorf("run --no-wait reported status %q, want QUEUED", noWait.Status)
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var status string
	var pid int
	if err := db.DB.QueryRow(
		`SELECT status, COALESCE(pid, 0) FROM workflow_executions WHERE id = ?`, noWait.ExecutionID,
	).Scan(&status, &pid); err != nil {
		t.Fatalf("read execution row: %v", err)
	}
	if status != "QUEUED" {
		t.Errorf("persisted status = %q, want QUEUED (the CLI must not run it locally)", status)
	}
	if pid != 0 {
		t.Errorf("persisted pid = %d, want 0 (unowned row awaiting adoption)", pid)
	}
}

// TestNormalizeLegacyWorkflowJSON_Passthrough: JSON without legacy keys
// must pass through byte-identical (no re-marshal that could reorder keys).
func TestNormalizeLegacyWorkflowJSON_Passthrough(t *testing.T) {
	out, err := normalizeLegacyWorkflowJSON([]byte(validWorkflowFile))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if string(out) != validWorkflowFile {
		t.Fatalf("native JSON must pass through unchanged, got: %q", out)
	}
}

// workflow_nodes.id is globally unique across workflows, so importing two
// workflows that share node ids (every bundled example uses "trigger") used
// to fail with "UNIQUE constraint failed: workflow_nodes.id". The second
// import must succeed with colliding ids deterministically remapped across
// nodes and connections, edges preserved.
func TestWorkflowImportNodeIDCollision(t *testing.T) {
	// Isolate the hybrid store's file half (~/.monoagent/workflows).
	t.Setenv("HOME", t.TempDir())

	dbPath := filepath.Join(t.TempDir(), "import.db")
	cfg := &globalConfig{DBPath: dbPath}

	importOne := func(name string) string {
		t.Helper()
		content := fmt.Sprintf(`{
  "name": %q,
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "set", "type": "core.set", "name": "Set", "position": {"x": 1, "y": 0}, "config": {"fields": {}}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"}
  ]
}`, name)
		path := filepath.Join(t.TempDir(), "workflow.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write temp workflow: %v", err)
		}
		cmd := newWorkflowImportCmd(cfg)
		cmd.SetArgs([]string{"--file", path})
		return captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("import %s: %v", name, err)
			}
		})
	}

	if out := importOne("collision-a"); strings.Contains(out, "Remapped") {
		t.Fatalf("first import should not remap any ids, got: %q", out)
	}
	out := importOne("collision-b")
	if !strings.Contains(out, "trigger → trigger-2") || !strings.Contains(out, "set → set-2") {
		t.Fatalf("second import should note remapped node ids, got: %q", out)
	}
	if !strings.Contains(out, "c1 → c1-2") {
		t.Fatalf("second import should note remapped connection ids, got: %q", out)
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Both workflows' nodes present, all ids distinct.
	var nodeCount, distinctIDs int
	if err := db.DB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT id) FROM workflow_nodes`).Scan(&nodeCount, &distinctIDs); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if nodeCount != 4 || distinctIDs != 4 {
		t.Fatalf("expected 4 nodes with 4 distinct ids, got %d nodes / %d distinct", nodeCount, distinctIDs)
	}

	// The second workflow's nodes carry the remapped ids.
	var bNodes string
	if err := db.DB.QueryRow(`SELECT group_concat(id) FROM workflow_nodes
		WHERE workflow_id = (SELECT id FROM workflows WHERE name = 'collision-b')`).Scan(&bNodes); err != nil {
		t.Fatalf("load collision-b nodes: %v", err)
	}
	if !strings.Contains(bNodes, "trigger-2") || !strings.Contains(bNodes, "set-2") {
		t.Fatalf("collision-b nodes should be remapped, got: %q", bNodes)
	}

	// Edges preserved: each workflow has one edge whose endpoints are its own nodes.
	var edgeCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_connections c
		JOIN workflow_nodes s ON s.id = c.source_node_id AND s.workflow_id = c.workflow_id
		JOIN workflow_nodes g ON g.id = c.target_node_id AND g.workflow_id = c.workflow_id`).Scan(&edgeCount); err != nil {
		t.Fatalf("count preserved edges: %v", err)
	}
	if edgeCount != 2 {
		t.Fatalf("expected 2 preserved edges (one per workflow), got %d", edgeCount)
	}
}

// duplicateConnectionWorkflowFile has two distinct connection ids (c1, c2)
// wiring the exact same edge (trigger.main → set.main). This passes
// workflow.ValidateConnectionsAgainstNodes (which only checks that the
// referenced node ids exist) and the import command's id-collision remap
// (c1 and c2 don't collide with each other or anything already stored), so
// it reaches persistence unchanged — then fails at the real INSERT because
// workflow_connections has UNIQUE(source_node_id, source_handle,
// target_node_id, target_handle) (data/migrations/006_workflow_system.sql).
// This is the "actual failure after persistence starts" case: by the time
// SaveWorkflowConnections fails, CreateWorkflow and SaveWorkflowNodes have
// already committed.
const duplicateConnectionWorkflowFile = `{
  "name": "dup-conn-wf",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "set", "type": "core.set", "name": "Set", "position": {"x": 1, "y": 0}, "config": {"fields": {}}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"},
    {"id": "c2", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"}
  ]
}`

// TestWorkflowImportAtomicity_FreshCreateCleansUpOnPersistFailure guards the
// atomic-import fix for a brand-new workflow (no --overwrite, nothing to
// restore): when persistence fails partway through (see
// duplicateConnectionWorkflowFile above), createOrOverwriteWorkflowAtomically
// must delete the workflow row, its nodes, and its file — not leave orphaned
// nodes behind just because their connections rolled back.
func TestWorkflowImportAtomicity_FreshCreateCleansUpOnPersistFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "atomic-fresh.db")
	cfg := &globalConfig{DBPath: dbPath, ProfileID: "default"}

	path := writeTempWorkflow(t, duplicateConnectionWorkflowFile)
	cmd := newWorkflowImportCmd(cfg)
	cmd.SetArgs([]string{"--file", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected import of a duplicate-edge connection set to fail")
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"workflows", "workflow_nodes", "workflow_connections"} {
		var n int
		if err := db.DB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("expected zero rows in %s after a failed fresh import, got %d — orphaned rows leaked", table, n)
		}
	}

	entries, err := os.ReadDir(filepath.Join(home, ".monoagent", "workflows"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read workflows dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("expected no leftover workflow file after a failed fresh import, found %s", e.Name())
		}
	}
}

// TestWorkflowImportAtomicity_OverwriteRestoresPreviousOnPersistFailure
// guards the --overwrite half of the same fix: a failed overwrite must
// restore the previous workflow (metadata, nodes, connections) exactly, and
// must not touch its execution history, even though SaveWorkflowNodes had
// already overwritten the SQL node rows with the bad version's names before
// SaveWorkflowConnections failed and forced the rollback.
func TestWorkflowImportAtomicity_OverwriteRestoresPreviousOnPersistFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "atomic-overwrite.db")
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}

	const v1 = `{
  "id": "stable-wf-id",
  "name": "overwrite-wf-v1",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "set", "type": "core.set", "name": "Set", "position": {"x": 1, "y": 0}, "config": {"fields": {}}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"}
  ]
}`
	var imported struct {
		ID string `json:"id"`
	}
	// --overwrite on the very first import of this id is what makes the CLI
	// honor the file's explicit "id" instead of assigning a fresh UUID (see
	// newWorkflowImportCmd: "Assign a fresh ID unless --overwrite is
	// requested and one is present") — needed here so the second import
	// below can target this exact workflow.
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "import", "--file", writeTempWorkflow(t, v1), "--overwrite")), &imported); err != nil {
		t.Fatalf("parse v1 import output: %v", err)
	}
	if imported.ID != "stable-wf-id" {
		t.Fatalf("expected v1 to keep its explicit id, got %q", imported.ID)
	}

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.DB.Exec(
		`INSERT INTO workflow_executions (id, workflow_id, profile_id, status, trigger_type, error_message, created_at)
		 VALUES ('exec-1', 'stable-wf-id', 'default', 'SUCCESS', 'manual', '', CURRENT_TIMESTAMP)`,
	); err != nil {
		t.Fatalf("seed execution history: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// Same id, --overwrite, but with the duplicate-edge bug: node names
	// differ from v1 (Manual2/Set2) so a leaked partial write is detectable,
	// and reusing the same node ids means SaveWorkflowNodes for v2 succeeds
	// (an upsert by id) before SaveWorkflowConnections fails.
	const v2Bad = `{
  "id": "stable-wf-id",
  "name": "overwrite-wf-v2-BAD",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual2", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "set", "type": "core.set", "name": "Set2", "position": {"x": 1, "y": 0}, "config": {"fields": {}}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"},
    {"id": "c2", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"}
  ]
}`
	cmd := newWorkflowImportCmd(cfg)
	cmd.SetArgs([]string{"--file", writeTempWorkflow(t, v2Bad), "--overwrite"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected the bad --overwrite to fail")
	}

	db2, err := initDB(cfg)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()
	store := openWorkflowStore(db2)
	ctx := context.Background()

	restored, err := store.GetWorkflow(ctx, "stable-wf-id")
	if err != nil || restored == nil {
		t.Fatalf("workflow missing after failed overwrite: %v", err)
	}
	if restored.Name != "overwrite-wf-v1" {
		t.Fatalf("expected v1's name restored, got %q", restored.Name)
	}
	if len(restored.Connections) != 1 || restored.Connections[0].ID != "c1" {
		t.Fatalf("expected exactly v1's single connection restored, got %+v", restored.Connections)
	}

	var nodeNames string
	if err := db2.DB.QueryRow(
		`SELECT group_concat(name) FROM workflow_nodes WHERE workflow_id = 'stable-wf-id' ORDER BY name`,
	).Scan(&nodeNames); err != nil {
		t.Fatalf("query restored nodes: %v", err)
	}
	if strings.Contains(nodeNames, "Manual2") || strings.Contains(nodeNames, "Set2") {
		t.Fatalf("v2's bad node names leaked into the restored workflow: %q", nodeNames)
	}
	if !strings.Contains(nodeNames, "Manual") || !strings.Contains(nodeNames, "Set") {
		t.Fatalf("v1's nodes were not restored, got: %q", nodeNames)
	}

	execs, err := store.ListExecutions(ctx, "stable-wf-id", 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(execs) != 1 || execs[0].ID != "exec-1" || execs[0].Status != "SUCCESS" {
		t.Fatalf("execution history not preserved across the failed overwrite, got %+v", execs)
	}
}

// TestWorkflowImportAtomicity_SuccessfulOverwriteReplacesContent is the
// happy-path companion to the two failure tests above: a valid --overwrite
// must fully replace the previous workflow's nodes and connections, not
// merge with or leave any of them behind.
func TestWorkflowImportAtomicity_SuccessfulOverwriteReplacesContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "atomic-overwrite-ok.db")
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}

	const v1 = `{
  "id": "stable-wf-id-2",
  "name": "ok-overwrite-v1",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "set", "type": "core.set", "name": "Set", "position": {"x": 1, "y": 0}, "config": {"fields": {}}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "set", "target_handle": "main"}
  ]
}`
	// --overwrite here too, so the explicit "id" in v1's file is honored —
	// otherwise the second import's --overwrite would target a workflow id
	// that was never actually created, silently becoming a fresh create
	// instead of a genuine overwrite of v1's row.
	runWorkflowSubcmd(t, cfg, "import", "--file", writeTempWorkflow(t, v1), "--overwrite")

	const v2 = `{
  "id": "stable-wf-id-2",
  "name": "ok-overwrite-v2",
  "nodes": [
    {"id": "trigger", "type": "trigger.manual", "name": "Manual", "position": {"x": 0, "y": 0}, "config": {}},
    {"id": "extra", "type": "core.set", "name": "Extra", "position": {"x": 2, "y": 0}, "config": {"fields": {}}}
  ],
  "connections": [
    {"id": "c1", "source": "trigger", "source_handle": "main", "target": "extra", "target_handle": "main"}
  ]
}`
	runWorkflowSubcmd(t, cfg, "import", "--file", writeTempWorkflow(t, v2), "--overwrite")

	db, err := initDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store := openWorkflowStore(db)
	ctx := context.Background()

	wf, err := store.GetWorkflow(ctx, "stable-wf-id-2")
	if err != nil || wf == nil {
		t.Fatalf("workflow missing after successful overwrite: %v", err)
	}
	if wf.Name != "ok-overwrite-v2" {
		t.Fatalf("expected v2's name, got %q", wf.Name)
	}
	if len(wf.Nodes) != 2 {
		t.Fatalf("expected exactly v2's 2 nodes, got %d", len(wf.Nodes))
	}
	var setStillPresent bool
	for _, n := range wf.Nodes {
		if n.ID == "set" {
			setStillPresent = true
		}
	}
	if setStillPresent {
		t.Fatalf("v1's dropped 'set' node should not survive a successful overwrite, nodes: %+v", wf.Nodes)
	}
	if len(wf.Connections) != 1 || wf.Connections[0].TargetNodeID != "extra" {
		t.Fatalf("expected v2's single connection to 'extra', got %+v", wf.Connections)
	}
}

// TestWorkflowTemplatesUse_PersistsSuccessfully is a light happy-path check
// that `workflow templates use` exercises the same
// createOrOverwriteWorkflowAtomically path as `workflow import` (see
// newWorkflowTemplatesUseCmd) and actually persists a usable workflow —
// the atomicity guarantees above apply here for free since it's the same
// shared helper, not a separate implementation.
func TestWorkflowTemplatesUse_PersistsSuccessfully(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "templates-use.db")
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}

	tmpls := workflow.ListTemplates()
	if len(tmpls) == 0 {
		t.Skip("no bundled templates registered")
	}

	var created struct {
		ID    string `json:"id"`
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	out := runWorkflowSubcmd(t, cfg, "templates", "use", tmpls[0].ID)
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("parse templates use output: %v (out: %q)", err, out)
	}
	if created.ID == "" || len(created.Nodes) == 0 {
		t.Fatalf("expected a persisted workflow with nodes, got: %q", out)
	}

	db, err := initDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store := openWorkflowStore(db)
	wf, err := store.GetWorkflow(context.Background(), created.ID)
	if err != nil || wf == nil {
		t.Fatalf("template-instantiated workflow not persisted: %v", err)
	}
	if len(wf.Nodes) != len(created.Nodes) {
		t.Fatalf("persisted node count %d != reported %d", len(wf.Nodes), len(created.Nodes))
	}
}

// A valid workflow has no warnings or issues; an unknown node type is an
// error (exit 3) whose issue keeps the hint about installing an automation.
func TestWorkflowValidateFile_UnknownNodeTypeIsAnError(t *testing.T) {
	out, err := runValidateCmd(t, true, "--file", writeTempWorkflow(t, validWorkflowFile))
	if err != nil {
		t.Fatal(err)
	}
	var got validationJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 0 || len(got.Issues) != 0 {
		t.Fatalf("valid workflow got warnings/issues: %+v", got)
	}

	unknown := strings.Replace(validWorkflowFile, `"type": "core.set"`, `"type": "acme-crm.list_deals"`, 1)
	out, err = runValidateCmd(t, true, "--file", writeTempWorkflow(t, unknown))
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("unknown node type: exit %d (%v), want 3", code, err)
	}
	got = validationJSON{}
	// The first JSON value; cobra's usage text may follow the error.
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Valid || len(got.Errors) != 1 || !strings.Contains(got.Errors[0], "acme-crm.list_deals") {
		t.Fatalf("want invalid with one error naming the type, got %+v", got)
	}
	if len(got.Issues) != 1 || got.Issues[0].Code != "unknown_node_type" || got.Issues[0].NodeID != "set" ||
		!strings.Contains(got.Issues[0].Hint, "automation") {
		t.Fatalf("want one unknown_node_type issue with the install hint, got %+v", got.Issues)
	}
}

// The schema-aware repro: a typo'd key, a method outside its options and
// a reference to a node that does not exist.
func TestWorkflowValidateFile_SchemaIssues(t *testing.T) {
	const repro = `{"name": "repro", "nodes": [
  {"id": "t", "type": "trigger.manual", "name": "Start", "config": {}},
  {"id": "h", "type": "http.request", "name": "Fetch", "config": {"urll": "https://x", "method": "FETCH"}},
  {"id": "s", "type": "core.set", "name": "Set", "config": {"value": "{{ $node[\"Nope\"].json.x }}"}}
], "connections": [
  {"id": "c1", "source": "t", "target": "h"}, {"id": "c2", "source": "h", "target": "s"}
]}`
	out, err := runValidateCmd(t, true, "--file", writeTempWorkflow(t, repro))
	if code := exitCodeFor(err); code != 3 {
		t.Fatalf("exit %d (%v), want 3", code, err)
	}
	var got validationJSON
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&got); err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, is := range got.Issues {
		codes[is.Code+"/"+is.NodeID+"/"+is.Field] = true
	}
	for _, want := range []string{"missing_required/h/url", "invalid_option/h/method", "unknown_field/h/urll", "dangling_node_ref/s/value"} {
		if !codes[want] {
			t.Errorf("missing issue %s in %+v", want, got.Issues)
		}
	}
	if got.Valid || len(got.Errors) == 0 || len(got.Warnings) == 0 {
		t.Errorf("want invalid with errors and warnings, got %+v", got)
	}
}
