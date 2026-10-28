package workflowio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

const monoFile = `{"name":"io-test","nodes":[
  {"id":"t","type":"trigger.manual","name":"T","position":{"x":0,"y":0},"config":{}},
  {"id":"s","type":"core.set","name":"S","position":{"x":1,"y":2},"config":{"value":"x"}}],
  "connections":[{"id":"c","source":"t","target":"s"}]}`

const legacyFile = `{"name":"legacy","nodes":[{"id":"t","node_type":"trigger.manual","name":"T","position_x":3,"position_y":4}],
  "connections":[{"id":"c","source_node_id":"t","target_node_id":"t"}]}`

func TestParseWorkflowDefinition(t *testing.T) {
	t.Parallel()
	d := Deps{InvalidInput: errorOf(3)}
	wf, err := d.ParseWorkflowDefinition([]byte(monoFile))
	if err != nil || len(wf.Nodes) != 2 || wf.Connections[0].SourceNodeID != "t" {
		t.Fatalf("native = %+v, %v", wf, err)
	}
	wf, err = d.ParseWorkflowDefinition([]byte(legacyFile))
	if err != nil || wf.Nodes[0].Type != "trigger.manual" || wf.Nodes[0].PositionX != 3 || wf.Connections[0].TargetNodeID != "t" {
		t.Fatalf("legacy = %+v, %v", wf, err)
	}
	if out, err := NormalizeLegacyWorkflowJSON([]byte(monoFile)); err != nil || string(out) != monoFile {
		t.Fatalf("native JSON must pass through unchanged: %q, %v", out, err)
	}
	for _, bad := range []string{`not json`, `{"nodes":[{"id":"n1","name":"x"}]}`} {
		if _, err := d.ParseWorkflowDefinition([]byte(bad)); exitCode(err) != 3 {
			t.Errorf("%s: exit %d (%v), want 3", bad, exitCode(err), err)
		}
	}
}

// import is idempotent: the same file again is "unchanged", --as-new copies, export prints the
// documented file shape, and errors keep their exit codes.
func TestImportExport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := newTestConfig(t)
	d := testDeps(t, cfg)
	importID := func(args ...string) (string, string) {
		t.Helper()
		_, out, _, err := run(t, d, monoFile, append([]string{"import"}, args...)...)
		var res struct{ ID, Status string }
		if err != nil || json.Unmarshal([]byte(out), &res) != nil {
			t.Fatalf("import %v = %q, %v", args, out, err)
		}
		return res.ID, res.Status
	}
	id, status := importID()
	if status != ImportCreated {
		t.Fatalf("first import: %s", status)
	}
	if again, status := importID(); again != id || status != ImportUnchanged {
		t.Fatalf("re-import: %s %s, want %s unchanged", again, status, id)
	}
	if cp, status := importID("--as-new"); cp == id || status != ImportCreated {
		t.Fatalf("--as-new: %s %s", cp, status)
	}
	idx, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".monoagent", "workflow-imports.json"))
	if err != nil || !strings.Contains(string(idx), `"source": "stdin"`) {
		t.Fatalf("import index: %s, %v", idx, err)
	}

	out, _, _, err := run(t, d, "", "export", id)
	var file workflow.WorkflowFile
	if err != nil || json.Unmarshal([]byte(out), &file) != nil || len(file.Nodes) != 2 || file.Connections[0].Source == "" {
		t.Fatalf("export = %q, %v", out, err)
	}
	dst := filepath.Join(t.TempDir(), "out.json")
	if out, _, _, err = run(t, d, "", "export", id, "-o", dst); err != nil || out != `Exported workflow "io-test" to `+dst+"\n" {
		t.Fatalf("export -o = %q, %v", out, err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		stdin string
		args  []string
		code  int
	}{
		{"", []string{"export", "nope"}, 2},
		{"", []string{"import", "--file", "/nonexistent/x.json"}, 2},
		{monoFile, []string{"import", "--replace", "nope"}, 2},
		{"{", []string{"import"}, 3},
		{"", []string{"import", "--from", "zapier", "x.json"}, 3},
		{"", []string{"export", id, "--bundle-automations", "--automation-domains", "bad"}, 3},
		{"", []string{"export", id, "--bundle-automations", "--automation-domains", "typo=x.com"}, 3},
	} {
		if _, _, _, err := run(t, d, tc.stdin, tc.args...); exitCode(err) != tc.code {
			t.Errorf("%v: exit %d (%v), want %d", tc.args, exitCode(err), err, tc.code)
		}
	}
}

// convert prints the converted workflow and the report and saves nothing; unmapped nodes are a
// reported exit 3.
func TestConvert(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t)
	cfg.JSONOutput = false
	d := testDeps(t, cfg)
	_, out, errOut, err := run(t, d, "", "convert", "--from", "n8n", n8nFixture(t, "rss_to_slack.json"))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if wf, err := workflow.ParseWorkflowFileBytes([]byte(out)); err != nil || wf.Name != "RSS digest to Slack" {
		t.Fatalf("stdout is not the workflow: %v\n%s", err, out)
	}
	if !strings.Contains(errOut, "n8n import report") {
		t.Errorf("report on stderr: %q", errOut)
	}
	if _, err := os.Stat(cfg.DBPath); err == nil {
		t.Error("convert opened the database")
	}
	_, _, _, err = run(t, d, "", "convert", n8nFixture(t, "unmapped.json"))
	var rep reported
	if exitCode(err) != 3 || !errors.As(err, &rep) {
		t.Fatalf("unmapped: exit %d (%v), want a reported exit 3", exitCode(err), err)
	}
	// QA-A U6: every node maps, but a kept n8n expression fails validate:
	// not "complete", and a reported exit 3.
	_, _, errOut, err = run(t, d, n8nKeptExpression, "convert", "-")
	if exitCode(err) != 3 || !errors.As(err, &rep) || !strings.Contains(errOut, "result: converted, 1 validation error(s)") {
		t.Fatalf("kept expression: exit %d (%v), want a reported exit 3\n%s", exitCode(err), err, errOut)
	}
	if _, _, _, err := run(t, d, `{"nodes": []}`, "convert", "-"); exitCode(err) != 3 {
		t.Errorf("not a workflow: exit %d (%v), want 3", exitCode(err), err)
	}
	if _, _, _, err := run(t, d, "", "convert", "a.json", "--file", "b.json"); exitCode(err) != 3 {
		t.Errorf("file twice: exit %d (%v), want 3", exitCode(err), err)
	}
}

// n8nKeptExpression is an n8n workflow whose Set node keeps an expression
// mono-agent's templates cannot parse.
const n8nKeptExpression = `{"name":"kept","nodes":[
{"id":"1","name":"Start","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
{"id":"2","name":"Count","type":"n8n-nodes-base.set","typeVersion":3.4,"position":[200,0],"parameters":{"assignments":{"assignments":[{"id":"1","name":"n","value":"={{ $('Start').all().length }}","type":"number"}]},"options":{}}}
],"connections":{"Start":{"main":[[{"node":"Count","type":"main","index":0}]]}}}`

func TestImportFromN8n(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := newTestConfig(t)
	_, out, _, err := run(t, testDeps(t, cfg), "", "import", "--from", "n8n", n8nFixture(t, "if_switch_merge.json"))
	var res struct {
		ID, Status string
		N8n        struct{ Complete bool } `json:"n8n"`
	}
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.ID == "" || res.Status != ImportCreated {
		t.Fatalf("import --from n8n = %q, %v", out, err)
	}
}

// migrate copies SQLite workflows into ~/.monoagent/workflows and leaves SQLite alone.
func TestMigrate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := newTestConfig(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := workflow.NewSQLiteWorkflowStore(db.DB).CreateWorkflow(context.Background(), &workflow.Workflow{ProfileID: "default", ID: "m1", Name: "M", Version: 1}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, _, _, err := run(t, testDeps(t, cfg), "", "migrate")
	if err != nil || !strings.Contains(out, "Found 1 workflows in SQLite.") || !strings.Contains(out, "  OK   m1 (M)\n") ||
		!strings.Contains(out, "Migration complete: 1 migrated, 0 skipped.") {
		t.Fatalf("migrate = %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "workflows", "m1.json")); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedWorkflowAndFile(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig(t)
	d := testDeps(t, cfg)
	db, err := d.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := d.WorkflowStore(db)
	ctx := context.Background()
	for _, p := range []string{"p1", "p2"} {
		if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES (?, ?)`, p, p); err != nil {
			t.Fatal(err)
		}
	}
	wf := &workflow.Workflow{ID: "o1", Name: "O", Version: 1, ProfileID: "p1",
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "T", PositionX: 5}}}
	if err := d.CreateWorkflow(ctx, store, wf, false); err != nil {
		t.Fatal(err)
	}
	if OwnedWorkflow(ctx, store, db.DB, "p1", "o1") == nil {
		t.Error("the owner does not see its workflow")
	}
	if OwnedWorkflow(ctx, store, db.DB, "p2", "o1") != nil || OwnedWorkflow(ctx, store, db.DB, "p1", "") != nil || OwnedWorkflow(ctx, store, db.DB, "", "o1") != nil {
		t.Error("another profile (or an empty id, or no profile) sees the workflow")
	}
	file := WorkflowFileFromWorkflow(wf)
	if file.ID != "o1" || file.Nodes[0].Type != "trigger.manual" || file.Nodes[0].Position.X != 5 {
		t.Errorf("file = %+v", file)
	}
}
