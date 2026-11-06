package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// approvalsTestDB is a migrated database (foreign keys on, as the CLI opens
// it) and its SQLite store.
func approvalsTestDB(t *testing.T) (*storage.Database, *SQLiteWorkflowStore) {
	t.Helper()
	db := testdb.Open(t)
	return db, NewSQLiteWorkflowStore(db.DB)
}

func approvalsTestWorkflow(id string, n int) *Workflow {
	return &Workflow{ID: id, Name: id, ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: id + "-t", WorkflowID: id, Type: "trigger.schedule", Name: "Every hour"},
			{ID: id + "-s", WorkflowID: id, Type: "core.set", Name: "Set", Config: map[string]interface{}{"n": n}},
		},
		Connections: []WorkflowConnection{{ID: id + "-c", WorkflowID: id, SourceNodeID: id + "-t", SourceHandle: "main", TargetNodeID: id + "-s", TargetHandle: "main"}},
	}
}

func createSQLWorkflow(t *testing.T, s *SQLiteWorkflowStore, wf *Workflow) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWorkflowConnections(ctx, wf.ID, wf.Connections); err != nil {
		t.Fatal(err)
	}
}

// approved reports whether the definition the engine would run for id (the
// file's, else SQLite's) is approved.
func approved(t *testing.T, h *HybridWorkflowStore, id string) bool {
	t.Helper()
	ctx := context.Background()
	wf, err := h.GetWorkflow(ctx, id)
	if err != nil || wf == nil {
		t.Fatalf("workflow %s: %v", id, err)
	}
	hash, err := definitionHash(wf)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := h.DefinitionApproved(ctx, id, hash)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// The first open after approvals arrive approves the definition every
// workflow has then, not only the ones a version recorded: one never run or
// saved through the store, and one whose file was edited by hand. It runs
// once; what changes afterwards is not approved by it (#831 round 4, R1).
func TestBackfillApprovesTheCurrentDefinitions(t *testing.T) {
	db, sqlStore := approvalsTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	approvalsDir = dir
	t.Cleanup(func() { approvalsDir = "" })
	files, err := NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHybridWorkflowStore(files, sqlStore)

	createSQLWorkflow(t, sqlStore, approvalsTestWorkflow("wf-unversioned", 1))
	createSQLWorkflow(t, sqlStore, approvalsTestWorkflow("wf-edited", 1))
	if err := files.SaveWorkflow(ctx, approvalsTestWorkflow("wf-edited", 2)); err != nil { // the file, edited by hand
		t.Fatal(err)
	}
	for _, id := range []string{"wf-unversioned", "wf-edited"} {
		if approved(t, h, id) {
			t.Fatalf("%s approved before the backfill", id)
		}
	}
	if err := BackfillApprovals(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wf-unversioned", "wf-edited"} {
		if !approved(t, h, id) {
			t.Errorf("%s: its current definition was not approved", id)
		}
	}

	// Once: a later change, by anyone, is not approved by it.
	if err := files.SaveWorkflow(ctx, approvalsTestWorkflow("wf-edited", 3)); err != nil {
		t.Fatal(err)
	}
	if err := BackfillApprovals(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	if approved(t, h, "wf-edited") {
		t.Error("a definition changed after the backfill was approved by it")
	}
}

// A workflow whose file can't be read is skipped: the others are approved,
// and the step is done (#831 round 5, F5).
func TestBackfillSkipsAWorkflowItCantRead(t *testing.T) {
	db, sqlStore := approvalsTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	approvalsDir = dir
	t.Cleanup(func() { approvalsDir = "" })
	files, err := NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHybridWorkflowStore(files, sqlStore)
	createSQLWorkflow(t, sqlStore, approvalsTestWorkflow("wf-a-corrupt", 1))
	createSQLWorkflow(t, sqlStore, approvalsTestWorkflow("wf-b-fine", 1))
	if err := os.WriteFile(filepath.Join(dir, "wf-a-corrupt.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BackfillApprovals(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	if !approved(t, h, "wf-b-fine") {
		t.Error("a workflow after one that can't be read was not approved")
	}
	var done int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, approvalsBackfilledKey).Scan(&done); err != nil || done != 1 {
		t.Errorf("the backfill is not marked done: %v, %d", err, done)
	}
}

// A workflow that is only a file at the backfill (the desktop editor's, an
// older CLI's: no SQLite row to approve yet) is approved when its file is
// first synced, with the definition it had at the backfill; one whose file
// changed in between is not (#831 round 5, C1).
func TestBackfillApprovesFileOnlyWorkflowsWhenTheySync(t *testing.T) {
	db, sqlStore := approvalsTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	approvalsDir = dir
	t.Cleanup(func() { approvalsDir = "" })
	files, err := NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHybridWorkflowStore(files, sqlStore)
	for _, id := range []string{"wf-file", "wf-file-edited"} {
		if err := files.SaveWorkflow(ctx, approvalsTestWorkflow(id, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := BackfillApprovals(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	if err := files.SaveWorkflow(ctx, approvalsTestWorkflow("wf-file-edited", 2)); err != nil { // changed before its first sync
		t.Fatal(err)
	}
	sync := func(id string) {
		t.Helper()
		wf, err := files.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		createSQLWorkflow(t, sqlStore, wf)
		if err := ApproveBackfilledFile(ctx, db.DB, wf); err != nil {
			t.Fatal(err)
		}
	}
	sync("wf-file")
	sync("wf-file-edited")
	if !approved(t, h, "wf-file") {
		t.Error("a file-only workflow was not approved when it synced")
	}
	if approved(t, h, "wf-file-edited") {
		t.Error("a file-only workflow changed after the backfill was approved")
	}
	var left int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE ?`, approvalsBackfillFilePrefix+"%").Scan(&left); err != nil || left != 0 {
		t.Errorf("the backfill's records of synced files: %v, %d left", err, left)
	}
}

// A workflow's approvals go with it, whichever way it is deleted: the
// store's delete, or a delete of its row as the chat's and MCP's tools do
// (#831 round 4, R4).
func TestApprovalsGoWithTheirWorkflow(t *testing.T) {
	db, sqlStore := approvalsTestDB(t)
	ctx := context.Background()
	count := func(id string) int {
		t.Helper()
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_approvals WHERE workflow_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, id := range []string{"wf-store", "wf-raw"} {
		createSQLWorkflow(t, sqlStore, approvalsTestWorkflow(id, 1))
		if err := sqlStore.ApproveDefinition(ctx, id); err != nil {
			t.Fatal(err)
		}
		if count(id) != 1 {
			t.Fatalf("%s: %d approvals", id, count(id))
		}
	}
	if err := NewHybridWorkflowStore(nil, sqlStore).DeleteWorkflow(ctx, "wf-store"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`DELETE FROM workflows WHERE id = 'wf-raw'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wf-store", "wf-raw"} {
		if n := count(id); n != 0 {
			t.Errorf("%s: %d approvals outlive the workflow", id, n)
		}
	}
	// A workflow with no row (a file nothing synced) gets no approval row.
	if err := sqlStore.approve(ctx, approvalsTestWorkflow("wf-file-only", 1)); err != nil || count("wf-file-only") != 0 {
		t.Errorf("an approval without a workflow row: %v, %d rows", err, count("wf-file-only"))
	}
}
