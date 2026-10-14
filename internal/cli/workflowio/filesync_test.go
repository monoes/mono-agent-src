package workflowio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A file-only workflow is mirrored into SQLite once; the marker, kept next to the folder and
// never inside it, stops a re-sync until the file changes. No folder is nothing to sync.
func TestSyncFileWorkflowsToSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if synced, err := SyncFileWorkflowsToSQL(ctx, db.DB, filepath.Join(root, "none")); err != nil || synced != nil {
		t.Fatalf("no folder: %v %v", synced, err)
	}
	dir := filepath.Join(root, "workflows")
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	wf := &workflow.Workflow{ID: "fs-wf", Name: "file only", Version: 1, CreatedAt: now, UpdatedAt: now,
		Nodes: []workflow.WorkflowNode{{ID: "fs-trigger", WorkflowID: "fs-wf", Type: "trigger.manual", Name: "Start", Config: map[string]any{}}}}
	if err := files.SaveWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if synced, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil || len(synced) != 1 || synced[0] != "fs-wf" {
		t.Fatalf("first sync: %v %v", synced, err)
	}
	var typ string
	if err := db.DB.QueryRow(`SELECT node_type FROM workflow_nodes WHERE id = 'fs-trigger'`).Scan(&typ); err != nil || typ != "trigger.manual" {
		t.Fatalf("the node in SQLite: %q %v", typ, err)
	}
	if again, _ := SyncFileWorkflowsToSQL(ctx, db.DB, dir); len(again) != 0 {
		t.Errorf("the marker did not stop a re-sync: %v", again)
	}
	if _, err := os.Stat(filepath.Join(root, SyncMarkerName)); err != nil {
		t.Errorf("no marker next to the folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, SyncMarkerName)); err == nil {
		t.Error("the marker must not live inside the workflow folder")
	}
}
