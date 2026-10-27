package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newTestApp builds an App wired to a temp SQLite DB and temp workflow-file
// directory — the same hybrid store startup() constructs, without the Wails
// runtime. The active profile defaults to "default".
func newTestApp(t *testing.T) *App {
	t.Helper()
	sdb, err := storage.NewDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := sdb.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { sdb.DB.Close() })

	a := &App{
		db:          sdb.DB,
		dbPath:      filepath.Join(t.TempDir(), "monoagent.db"), // the UI state file lands beside it
		runningCmds: make(map[string]*exec.Cmd),
	}
	a.selectForTest("default") // a profile that happens to be called default; tests that need none clear it
	return a
}

// cliHomeDB opens the database a monoagentcli subprocess with HOME=home
// uses, once the CLI has created and migrated it.
func cliHomeDB(t *testing.T, home string) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(home, ".monoagent", "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestWorkflowBindingsThroughCLI drives the workflow bindings against the
// real CLI in an isolated HOME: the behaviour the bindings had when they
// used the store directly must survive their move behind the CLI.
func TestWorkflowBindingsThroughCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME isolation is unix-only")
	}
	cliBin := buildTestCLI(t) // before HOME moves, so the build cache stays put
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MONOAGENTCLI_BIN", cliBin)
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(home, "hb.json"))
	a := newTestApp(t)

	saved, err := a.SaveWorkflow(SaveWorkflowRequest{Name: "gui-saved", Description: "d", IsActive: true,
		Nodes: []WorkflowNodeData{
			{ID: "n1", NodeType: "trigger.manual", Name: "Start", PositionX: 1.5, PositionY: 2.5, Config: map[string]interface{}{}},
			{ID: "n2", NodeType: "core.set", Name: "Set", Config: map[string]interface{}{"field": "x"}},
		},
		Connections: []WorkflowConnectionData{{ID: "c1", SourceNodeID: "n1", TargetNodeID: "n2"}},
	})
	if err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}
	db := cliHomeDB(t, home)
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('other', 'Other', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}

	listed := func() map[string]WorkflowSummary {
		t.Helper()
		list, err := a.ListWorkflows()
		if err != nil {
			t.Fatalf("ListWorkflows: %v", err)
		}
		out := map[string]WorkflowSummary{}
		for _, s := range list {
			out[s.ID] = s
		}
		return out
	}

	// RA1-2/RA1-3: a saved workflow is listed before it ever ran, with its
	// node count; so is one that exists only as a file (`workflow create`).
	t.Run("ListedBeforeAnyRun", func(t *testing.T) {
		files, err := workflow.NewWorkflowFileStore(filepath.Join(home, ".monoagent", "workflows"))
		if err != nil {
			t.Fatal(err)
		}
		if err := files.SaveWorkflow(t.Context(), &workflow.Workflow{ID: "file-only-1", Name: "cli-created", ProfileID: "default"}); err != nil {
			t.Fatal(err)
		}
		got := listed()
		if s := got[saved.ID]; s.Name != "gui-saved" || !s.IsActive || s.NodeCount != 2 {
			t.Fatalf("saved workflow listed as %+v", s)
		}
		// A file that appeared by itself after the store was first used has no stored copy: it is
		// not listed (and not imported) until an operator adopts it, and is then inactive.
		if _, ok := got["file-only-1"]; ok {
			t.Fatalf("a file nobody adopted is listed: %+v", got)
		}
		hybrid := workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db.DB))
		adoptable, _, err := hybrid.AdoptableFiles(t.Context())
		if err != nil || len(adoptable) != 1 || adoptable[0].ID != "file-only-1" {
			t.Fatalf("adoptable files: %+v %v", adoptable, err)
		}
		if err := hybrid.AdoptFile(t.Context(), "file-only-1", adoptable[0].ProfileID, adoptable[0].SHA); err != nil {
			t.Fatal(err)
		}
		if s, ok := listed()["file-only-1"]; !ok || s.IsActive {
			t.Fatalf("an adopted file-only workflow is listed as %+v (listed %v)", s, ok)
		}
	})

	// One profile never sees, reads or exports another's workflow.
	t.Run("ProfileScoped", func(t *testing.T) {
		a.selectForTest("other")
		defer a.selectForTest("default")
		if _, ok := listed()[saved.ID]; ok {
			t.Fatal("workflow from profile 'default' leaked into 'other'")
		}
		if _, err := a.GetWorkflow(saved.ID); err == nil {
			t.Fatal("GetWorkflow returned another profile's workflow")
		}
		if _, err := a.ExportWorkflow(saved.ID); err == nil {
			t.Fatal("ExportWorkflow exported another profile's workflow")
		}
		if err := a.DeleteWorkflow(saved.ID); err == nil {
			t.Fatal("DeleteWorkflow deleted another profile's workflow")
		}
	})

	// The export is the documented WorkflowFile shape.
	t.Run("ExportShape", func(t *testing.T) {
		exported, err := a.ExportWorkflow(saved.ID)
		if err != nil {
			t.Fatal(err)
		}
		var f workflow.WorkflowFile
		if err := parseStrict(exported, &f); err != nil {
			t.Fatalf("export is not WorkflowFile JSON: %v\n%s", err, exported)
		}
		if f.ID != saved.ID || len(f.Nodes) != 2 || f.Nodes[0].Type != "trigger.manual" ||
			f.Nodes[0].Position.X != 1.5 || f.Nodes[0].Position.Y != 2.5 ||
			len(f.Connections) != 1 || f.Connections[0].Source != "n1" || f.Connections[0].Target != "n2" {
			t.Fatalf("export = %+v", f)
		}
	})

	// The editor auto-saves before every run and never sends is_active: an
	// edit must neither deactivate nor activate the workflow.
	t.Run("EditKeepsActivation", func(t *testing.T) {
		edited, err := a.SaveWorkflow(SaveWorkflowRequest{ID: saved.ID, Name: "gui-saved",
			Nodes: []WorkflowNodeData{{ID: "n1", NodeType: "trigger.manual", Name: "Start", Config: map[string]interface{}{}}}})
		if err != nil || !edited.IsActive {
			t.Fatalf("edit = %+v, %v", edited, err)
		}
		wf, err := a.GetWorkflow(saved.ID)
		if err != nil || !wf.IsActive || len(wf.Nodes) != 1 || len(wf.Connections) != 0 {
			t.Fatalf("after edit = %+v, %v", wf, err)
		}
		if err := a.SetWorkflowActive(saved.ID, false); err != nil {
			t.Fatalf("SetWorkflowActive: %v", err)
		}
		if _, err := a.SaveWorkflow(SaveWorkflowRequest{ID: saved.ID, Name: "gui-saved", IsActive: true}); err != nil {
			t.Fatal(err)
		}
		if wf, _ := a.GetWorkflow(saved.ID); wf == nil || wf.IsActive {
			t.Fatal("saving an edit activated a deactivated workflow")
		}
	})

	// RA1-7: running an inactive workflow is an error, not a silent flip.
	// RA1-11: a second run of a workflow already running is refused.
	t.Run("RunGuards", func(t *testing.T) {
		if err := a.RunWorkflow(saved.ID); err == nil || !strings.Contains(err.Error(), "inactive") {
			t.Fatalf("inactive run err = %v", err)
		}
		if err := a.SetWorkflowActive(saved.ID, true); err != nil {
			t.Fatal(err)
		}
		a.runningCmds[saved.ID] = &exec.Cmd{}
		defer delete(a.runningCmds, saved.ID)
		if err := a.RunWorkflow(saved.ID); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("concurrent run err = %v", err)
		}
	})

	// Execution detail keeps redacting secrets (vault.secret_get puts them
	// in the item stream), and a cancel marks the execution CANCELLED.
	t.Run("ExecutionDetailAndCancel", func(t *testing.T) {
		items, _ := json.Marshal([]workflow.Item{{JSON: map[string]interface{}{"api_key": "sk-live-super-secret-value", "label": "not secret"}}})
		if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, profile_id) VALUES ('exec-1', ?, 'RUNNING', 'default')`, saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`INSERT INTO workflow_execution_nodes (id, execution_id, node_id, node_name, status, input_items, output_items)
			VALUES ('node-1', 'exec-1', 'n1', 'Get Secret', 'SUCCESS', '[]', ?)`, string(items)); err != nil {
			t.Fatal(err)
		}
		poller := newTestApp(t) // polled bindings need a context; emitLog then needs Wails
		poller.ctx = context.Background()
		detail, err := poller.GetExecutionDetail("exec-1")
		if err != nil {
			t.Fatal(err)
		}
		nodes, _ := detail["nodes"].([]map[string]interface{})
		if len(nodes) != 1 {
			t.Fatalf("detail = %+v", detail)
		}
		out, _ := nodes[0]["output_items"].(string)
		if strings.Contains(out, "sk-live-super-secret-value") || !strings.Contains(out, "***") || !strings.Contains(out, "not secret") {
			t.Fatalf("output_items not redacted: %s", out)
		}

		if err := a.CancelWorkflow("exec-1"); err != nil {
			t.Fatal(err)
		}
		if detail, err := poller.GetExecutionDetail("exec-1"); err != nil || detail["status"] != "CANCELLED" {
			t.Fatalf("after cancel = %v, %v", detail["status"], err)
		}
		execs, err := poller.GetWorkflowExecutions(saved.ID, 0)
		if err != nil || len(execs) != 1 || execs[0].Status != "CANCELLED" || execs[0].FinishedAt == "" {
			t.Fatalf("GetWorkflowExecutions = %+v, %v", execs, err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		if err := a.DeleteWorkflow(saved.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := listed()[saved.ID]; ok {
			t.Fatal("deleted workflow still listed")
		}
		if _, err := os.Stat(filepath.Join(home, ".monoagent", "workflows", saved.ID+".json")); !os.IsNotExist(err) {
			t.Fatalf("workflow file left behind: %v", err)
		}
	})
}
