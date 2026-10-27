package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestHybridWorkflowStore_DeleteWorkflow_SurfacesFileCleanupError guards the
// DeleteWorkflow fix used as the compensating action after a failed atomic
// import (see createOrOverwriteWorkflowAtomically in
// cmd/monoagentcli/workflow.go): before this fix, DeleteWorkflow discarded
// every error from both halves unconditionally, so a caller using it as
// cleanup was told "success" even when a half genuinely failed to delete —
// exactly the scenario here, where the file half can't be removed (its
// directory is read-only) but the SQL half succeeds.
func TestHybridWorkflowStore_DeleteWorkflow_SurfacesFileCleanupError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write-permission removal does not block file deletion the same way on Windows")
	}
	dir := t.TempDir()
	files, err := NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatalf("NewWorkflowFileStore: %v", err)
	}
	sqlStore, rawDB := newMigratedStore(t)
	store := NewHybridWorkflowStore(files, sqlStore)
	ctx := context.Background()

	wf := &Workflow{ProfileID: "default", ID: "wf-cleanup", Name: "wf-cleanup"}
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	path := filepath.Join(dir, "wf-cleanup.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected workflow file to exist at %s: %v", path, err)
	}

	// Remove write+execute from the directory so os.Remove (called by
	// WorkflowFileStore.DeleteWorkflow) fails with permission denied,
	// regardless of the file's own permissions — deletion is governed by
	// the containing directory's permissions on POSIX, not the file's.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // let t.TempDir() clean up

	err = store.DeleteWorkflow(ctx, "wf-cleanup")
	if err == nil {
		t.Fatal("expected DeleteWorkflow to surface the file store's permission error, got nil")
	}
	if !errors.Is(err, ErrFileSyncFailed) {
		t.Fatalf("expected the error to say the file step failed, got: %v", err)
	}

	// The file must still be there — deletion genuinely failed, not just
	// reported as failed.
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected the leftover file to still exist after the failed delete: %v", statErr)
	}

	// A delete is one publication: when the file cannot go, the rows stay, so the workflow is
	// still whole (it used to be left as a file without rows) and no change is left open.
	var n int
	if scanErr := rawDB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'wf-cleanup'`).Scan(&n); scanErr != nil {
		t.Fatalf("count workflows row: %v", scanErr)
	}
	if n != 1 {
		t.Fatalf("expected the row to stay when the file could not be removed, got %d rows", n)
	}
	if openIntentCount(t, sqlStore) != 0 {
		t.Fatal("an unfinished change was left open")
	}
}

// The GUI lists through the hybrid store, so this is the path that produced
// "0 nodes" for every workflow. File-backed workflows are parsed whole and
// carry their nodes; SQL-backed ones need the grouped count query.
func TestHybridWorkflowStore_NodeCounts(t *testing.T) {
	dir := t.TempDir()
	files, err := NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatalf("NewWorkflowFileStore: %v", err)
	}
	sqlStore, rawDB := newMigratedStore(t)
	if _, err := rawDB.Exec(`INSERT INTO profiles (id, name) VALUES ('p1', 'p1')`); err != nil {
		t.Fatal(err)
	}
	store := NewHybridWorkflowStore(files, sqlStore)
	ctx := context.Background()

	// A file-backed workflow, saved with its nodes (an older version's file: taken in on first use).
	fileWF := &Workflow{
		ID: "from-file", Name: "from-file", ProfileID: "p1",
		Nodes: []WorkflowNode{testNode("a", "trigger.manual"), testNode("b", "gemini.chat_session")},
	}
	if err := files.SaveWorkflow(ctx, fileWF); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}

	// A SQL-only workflow with three nodes.
	if err := sqlStore.CreateWorkflow(ctx, &Workflow{ID: "from-sql", Name: "from-sql", ProfileID: "p1"}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := sqlStore.SaveWorkflowNodes(ctx, "from-sql", []WorkflowNode{
		testNode("n1", "trigger.manual"), testNode("n2", "http.request"), testNode("n3", "gemini.generate_image"),
	}); err != nil {
		t.Fatalf("SaveWorkflowNodes: %v", err)
	}

	counts, err := store.NodeCounts(ctx, "p1")
	if err != nil {
		t.Fatalf("NodeCounts: %v", err)
	}
	if counts["from-file"] != 2 {
		t.Errorf("file-backed count = %d, want 2", counts["from-file"])
	}
	if counts["from-sql"] != 3 {
		t.Errorf("sql-backed count = %d, want 3", counts["from-sql"])
	}
}
