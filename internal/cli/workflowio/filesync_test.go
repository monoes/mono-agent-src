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

// A file-only workflow is mirrored into SQLite once; the file's content, remembered in the
// database, stops a re-sync until the file changes. No folder is nothing to sync.
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
		t.Errorf("the remembered content did not stop a re-sync: %v", again)
	}
	var sha string
	if err := db.DB.QueryRow(`SELECT file_sha FROM workflow_file_state WHERE workflow_id = 'fs-wf'`).Scan(&sha); err != nil || sha == "" {
		t.Errorf("the file's content was not remembered: %q %v", sha, err)
	}
	if _, err := os.Stat(filepath.Join(root, SyncMarkerName)); err == nil {
		t.Error("the old marker file must no longer be written")
	}
}

// A file of a profile that does not exist is not mirrored: after the profile
// was deleted, a copy of its file must not come back as a row.
func TestSyncFileWorkflowsToSQLSkipsFilesOfAMissingProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := filepath.Join(t.TempDir(), "workflows")
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	wf := &workflow.Workflow{ID: "ghost-wf", Name: "ghost", ProfileID: "no-such-profile", Version: 1, CreatedAt: now, UpdatedAt: now,
		Nodes: []workflow.WorkflowNode{{ID: "g-trigger", WorkflowID: "ghost-wf", Type: "trigger.manual", Name: "Start", Config: map[string]any{}}}}
	if err := files.SaveWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if synced, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil || len(synced) != 0 {
		t.Fatalf("sync: %v %v, want nothing synced", synced, err)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'ghost-wf'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a workflow of a missing profile came back as a row")
	}
}

// An edit made outside the store is imported once, as a version, and not again.
func TestSyncFileWorkflowsToSQLImportsAnOutsideEditOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := filepath.Join(t.TempDir(), "workflows")
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db.DB))
	wf := &workflow.Workflow{ID: "edit-wf", Name: "edit", ProfileID: "default",
		Nodes: []workflow.WorkflowNode{{ID: "e-trigger", WorkflowID: "edit-wf", Type: "trigger.manual", Name: "Start", Config: map[string]any{}}}}
	if err := store.SaveWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if synced, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil || len(synced) != 0 {
		t.Fatalf("a file the store wrote was imported: %v %v", synced, err)
	}
	edited, _ := files.GetWorkflow(ctx, "edit-wf")
	edited.Nodes[0].Name = "Renamed by hand"
	if err := files.SaveWorkflow(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if synced, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil || len(synced) != 1 {
		t.Fatalf("the edit: %v %v", synced, err)
	}
	var name string
	if err := db.DB.QueryRow(`SELECT name FROM workflow_nodes WHERE id = 'e-trigger'`).Scan(&name); err != nil || name != "Renamed by hand" {
		t.Fatalf("the rows did not follow the file: %q %v", name, err)
	}
	if again, _ := SyncFileWorkflowsToSQL(ctx, db.DB, dir); len(again) != 0 {
		t.Fatalf("imported twice: %v", again)
	}
	// A file cut off mid-write is left alone.
	if err := os.WriteFile(filepath.Join(dir, "edit-wf.json"), []byte(`{"id":"edit-wf","nodes":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if synced, _ := SyncFileWorkflowsToSQL(ctx, db.DB, dir); len(synced) != 0 {
		t.Fatalf("a truncated file was imported: %v", synced)
	}
	if err := db.DB.QueryRow(`SELECT name FROM workflow_nodes WHERE id = 'e-trigger'`).Scan(&name); err != nil || name != "Renamed by hand" {
		t.Fatalf("rows changed: %q %v", name, err)
	}
}

// The first sync records which files are older versions'; a workflow file dropped in afterwards is
// not imported by a sync (it can name any profile): it waits for an operator to adopt it.
func TestSyncFileWorkflowsToSQLLeavesLaterFileOnlyWorkflowsToBeAdopted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := filepath.Join(t.TempDir(), "workflows")
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string) *workflow.Workflow {
		return &workflow.Workflow{ID: id, Name: id, ProfileID: "default", IsActive: true,
			Nodes: []workflow.WorkflowNode{{ID: id + "-t", WorkflowID: id, Type: "trigger.manual", Name: "Start", Config: map[string]any{}}}}
	}
	if err := files.SaveWorkflow(ctx, mk("old-wf")); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if err := files.SaveWorkflow(ctx, mk("new-wf")); err != nil {
		t.Fatal(err)
	}
	if synced, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil || len(synced) != 0 {
		t.Fatalf("%v %v", synced, err)
	}
	var n, active int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'new-wf'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a dropped file got rows (%d, %v)", n, err)
	}
	if err := db.DB.QueryRow(`SELECT is_active FROM workflows WHERE id = 'old-wf'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("the older version's workflow: active %d (%v)", active, err)
	}
}
