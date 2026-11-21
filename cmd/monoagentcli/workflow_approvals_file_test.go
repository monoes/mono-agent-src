package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow that is only a file in ~/.monoagent/workflows when approvals
// arrive (no SQLite row yet) is the user's from before, like every other:
// its first run after the update runs its command step (#831 round 5, C1).
func TestAFileOnlyWorkflowFromBeforeApprovalsRunsItsCommands(t *testing.T) {
	touch, err := exec.LookPath("touch")
	if err != nil {
		t.Skip("uses touch")
	}
	home, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	// A home from before approvals: a database whose backfill has not run,
	// and a workflow saved as a file only.
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	if _, err := db.DB.Exec(`DELETE FROM settings WHERE key = 'workflow_approvals.backfilled'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	files, err := workflow.NewWorkflowFileStore(filepath.Join(home, ".monoagent", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	wf := &workflow.Workflow{ID: "wf-file-only", Name: "From a file", ProfileID: "default", IsActive: true,
		Nodes: []workflow.WorkflowNode{
			{ID: "wf-file-only-t", WorkflowID: "wf-file-only", Type: "trigger.manual", Name: "Start"},
			{ID: "wf-file-only-c", WorkflowID: "wf-file-only", Type: "system.execute_command", Name: "Touch",
				Config: map[string]interface{}{"command": touch, "args": []interface{}{ran}}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "wf-file-only-l", WorkflowID: "wf-file-only",
			SourceNodeID: "wf-file-only-t", SourceHandle: "main", TargetNodeID: "wf-file-only-c", TargetHandle: "main"}},
	}
	if err := files.SaveWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}

	cfg.JSONOutput = false
	if _, out, err := runLogsFor(t, cfg, wf.ID); err != nil {
		t.Fatalf("the first run after the update: %v\n%s", err, out)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("a file-only workflow from before approvals did not run its command: %v", err)
	}
}
