package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// newMigratedStore opens a SQLite database in a temp dir, applies the real
// embedded migrations (foreign keys ON), and returns a store over it plus the
// raw *sql.DB for direct assertions.
func newMigratedStore(t *testing.T) (*SQLiteWorkflowStore, *sql.DB) {
	t.Helper()
	db := testdb.Open(t)
	return NewSQLiteWorkflowStore(db.DB), db.DB
}

func testNode(id, typ string) WorkflowNode {
	return WorkflowNode{
		ID:     id,
		Type:   typ,
		Name:   id,
		Config: map[string]interface{}{"k": "v"},
	}
}

func connCount(t *testing.T, db *sql.DB, workflowID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM workflow_connections WHERE workflow_id = ?`, workflowID).Scan(&n); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	return n
}

func connIDs(t *testing.T, s *SQLiteWorkflowStore, workflowID string) []string {
	t.Helper()
	wf, err := s.GetWorkflow(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	var ids []string
	for _, c := range wf.Connections {
		ids = append(ids, c.ID)
	}
	return ids
}

// nodeRow reads the raw stored columns for a node so tests can assert on
// exactly what the database kept (not the in-memory struct).
func nodeRow(t *testing.T, db *sql.DB, id string) (name, config string, x, y float64, createdAt string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT name, config, position_x, position_y, created_at FROM workflow_nodes WHERE id = ?`, id,
	).Scan(&name, &config, &x, &y, &createdAt); err != nil {
		t.Fatalf("read node %s: %v", id, err)
	}
	return
}

// TestSaveWorkflowConnections_DanglingReferenceGivesClearError is a
// regression guard: before this check existed, a connection naming a node
// id that doesn't exist for this workflow (a stale id, a typo in a
// hand-edited or generated workflow JSON) surfaced only via the SQLite
// foreign-key constraint itself — accurate but useless for finding which
// connection or node id was actually wrong.
func TestSaveWorkflowConnections_DanglingReferenceGivesClearError(t *testing.T) {
	s, _ := newMigratedStore(t)
	ctx := context.Background()

	if err := s.CreateWorkflow(ctx, &Workflow{ProfileID: "default", ID: "wf", Name: "wf"}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), testNode("b", "core.set"),
	}); err != nil {
		t.Fatalf("save nodes: %v", err)
	}

	err := s.SaveWorkflowConnections(ctx, "wf", []WorkflowConnection{
		{ID: "e1", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "does-not-exist", TargetHandle: "main"},
	})
	if err == nil {
		t.Fatalf("expected an error for a connection targeting an unknown node id")
	}
	if !errors.Is(err, ErrDanglingConnection) {
		t.Fatalf("error = %v, want errors.Is(err, ErrDanglingConnection)", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error should name the unknown node id, got: %v", err)
	}

	// The failed save must not have left a stray connection row behind.
	if n := connCount(t, s.db, "wf"); n != 0 {
		t.Fatalf("connection count after failed save = %d, want 0", n)
	}
}

// TestSaveWorkflowNodes_AddNodePreservesConnections is the regression guard
// for the delete-all+reinsert bug: saving the node set with one node appended
// must not touch the connections of the surviving nodes.
func TestSaveWorkflowNodes_AddNodePreservesConnections(t *testing.T) {
	s, _ := newMigratedStore(t)
	ctx := context.Background()

	if err := s.CreateWorkflow(ctx, &Workflow{ProfileID: "default", ID: "wf", Name: "wf"}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), testNode("b", "core.set"), testNode("c", "core.set"),
	}); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if err := s.SaveWorkflowConnections(ctx, "wf", []WorkflowConnection{
		{ID: "e1", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "b", TargetHandle: "main"},
		{ID: "e2", SourceNodeID: "b", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"},
	}); err != nil {
		t.Fatalf("save connections: %v", err)
	}
	before := connIDs(t, s, "wf")

	// Append a node and save — WITHOUT re-saving connections (no CLI
	// workaround): the store must preserve every existing edge.
	loaded, err := s.GetWorkflow(ctx, "wf")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	loaded.Nodes = append(loaded.Nodes, testNode("d", "core.set"))
	if err := s.SaveWorkflowNodes(ctx, "wf", loaded.Nodes); err != nil {
		t.Fatalf("save nodes with append: %v", err)
	}

	wf, err := s.GetWorkflow(ctx, "wf")
	if err != nil {
		t.Fatalf("GetWorkflow after append: %v", err)
	}
	if len(wf.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(wf.Nodes))
	}
	after := connIDs(t, s, "wf")
	if len(after) != len(before) {
		t.Fatalf("connections changed on node add: before=%v after=%v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("connection ids changed on node add: before=%v after=%v", before, after)
		}
	}
	if connCount(t, s.RawDB(), "wf") != 2 {
		t.Fatalf("expected 2 connections after node add, got %d", connCount(t, s.RawDB(), "wf"))
	}
}

// TestSaveWorkflowNodes_RemoveNodeCascadesOnlyItsEdges: dropping a node from
// the supplied set deletes that node's edges (FK cascade) and leaves every
// edge between surviving nodes intact.
func TestSaveWorkflowNodes_RemoveNodeCascadesOnlyItsEdges(t *testing.T) {
	s, _ := newMigratedStore(t)
	ctx := context.Background()

	if err := s.CreateWorkflow(ctx, &Workflow{ProfileID: "default", ID: "wf", Name: "wf"}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), testNode("b", "core.set"), testNode("c", "core.set"),
	}); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if err := s.SaveWorkflowConnections(ctx, "wf", []WorkflowConnection{
		{ID: "e1", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "b", TargetHandle: "main"},
		{ID: "e2", SourceNodeID: "b", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"},
		{ID: "e3", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"},
	}); err != nil {
		t.Fatalf("save connections: %v", err)
	}

	// Save without node c — no connection re-save; c's edges must cascade.
	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), testNode("b", "core.set"),
	}); err != nil {
		t.Fatalf("save nodes without c: %v", err)
	}

	wf, err := s.GetWorkflow(ctx, "wf")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if len(wf.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(wf.Nodes))
	}
	if len(wf.Connections) != 1 || wf.Connections[0].ID != "e1" {
		t.Fatalf("expected only edge e1 (a→b) to survive, got %+v", wf.Connections)
	}
}

// TestSaveWorkflowNodes_UpdateFieldsKeepsEdgesAndCreatedAt: updating a node's
// fields upserts the row, keeps its edges, and preserves the original
// created_at.
func TestSaveWorkflowNodes_UpdateFieldsKeepsEdgesAndCreatedAt(t *testing.T) {
	s, db := newMigratedStore(t)
	ctx := context.Background()

	if err := s.CreateWorkflow(ctx, &Workflow{ProfileID: "default", ID: "wf", Name: "wf"}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), testNode("b", "core.set"),
	}); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if err := s.SaveWorkflowConnections(ctx, "wf", []WorkflowConnection{
		{ID: "e1", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "b", TargetHandle: "main"},
	}); err != nil {
		t.Fatalf("save connections: %v", err)
	}
	_, _, _, _, createdAtBefore := nodeRow(t, db, "b")

	updated := testNode("b", "core.set")
	updated.Name = "renamed"
	updated.PositionX = 42
	updated.PositionY = 7
	updated.Disabled = true
	updated.Config = map[string]interface{}{"k": "changed"}
	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), updated,
	}); err != nil {
		t.Fatalf("save updated nodes: %v", err)
	}

	name, config, x, y, createdAtAfter := nodeRow(t, db, "b")
	if name != "renamed" || x != 42 || y != 7 || !strings.Contains(config, "changed") {
		t.Fatalf("node update not persisted: name=%q config=%q pos=(%v,%v)", name, config, x, y)
	}
	if createdAtAfter != createdAtBefore {
		t.Fatalf("created_at was reset on update: before=%q after=%q", createdAtBefore, createdAtAfter)
	}
	if got := connIDs(t, s, "wf"); len(got) != 1 || got[0] != "e1" {
		t.Fatalf("edge lost on node update: %v", got)
	}
	var disabled int
	if err := db.QueryRow(`SELECT disabled FROM workflow_nodes WHERE id='b'`).Scan(&disabled); err != nil {
		t.Fatalf("read disabled: %v", err)
	}
	if disabled != 1 {
		t.Fatalf("disabled flag not persisted, got %d", disabled)
	}
}

// TestSaveWorkflowNodes_EmptySetClearsAll preserves the previous semantics:
// saving an empty node set removes every node of the workflow (and, via FK
// cascade, every connection). A zero-node workflow stays valid.
func TestSaveWorkflowNodes_EmptySetClearsAll(t *testing.T) {
	s, _ := newMigratedStore(t)
	ctx := context.Background()

	if err := s.CreateWorkflow(ctx, &Workflow{ProfileID: "default", ID: "wf", Name: "wf"}); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if err := s.SaveWorkflowNodes(ctx, "wf", nil); err != nil {
		t.Fatalf("save nil nodes on fresh workflow: %v", err)
	}

	if err := s.SaveWorkflowNodes(ctx, "wf", []WorkflowNode{
		testNode("a", "trigger.manual"), testNode("b", "core.set"),
	}); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if err := s.SaveWorkflowConnections(ctx, "wf", []WorkflowConnection{
		{ID: "e1", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "b", TargetHandle: "main"},
	}); err != nil {
		t.Fatalf("save connections: %v", err)
	}

	if err := s.SaveWorkflowNodes(ctx, "wf", nil); err != nil {
		t.Fatalf("save nil nodes: %v", err)
	}
	if n := connCount(t, s.RawDB(), "wf"); n != 0 {
		t.Fatalf("expected 0 connections after empty save, got %d", n)
	}
	wf, err := s.GetWorkflow(ctx, "wf")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if len(wf.Nodes) != 0 {
		t.Fatalf("expected 0 nodes after empty save, got %d", len(wf.Nodes))
	}
}

// TestListWorkflowsUsesThePlainProfileIndex guards the ListWorkflows profile
// predicate: migration 099 drops 026's COALESCE(profile_id,'default')
// expression index, and `profile_id = ?` must be served by the plain
// idx_workflows_profile from 011.
func TestListWorkflowsUsesThePlainProfileIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations (fresh): %v", err)
	}
	var n int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_workflows_profile_default'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the COALESCE expression index must be gone (n=%d, %v)", n, err)
	}

	rows, err := db.DB.Query(
		`EXPLAIN QUERY PLAN SELECT id FROM workflows WHERE profile_id = ?`, "default")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan.WriteString(detail + "; ")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	if !strings.Contains(plan.String(), "idx_workflows_profile") {
		t.Fatalf("query plan does not use idx_workflows_profile: %s", plan.String())
	}

	// And ListWorkflows itself filters correctly through the store.
	store := NewSQLiteWorkflowStore(db.DB)
	ctx := context.Background()
	for _, w := range []struct {
		id, profile string
	}{
		{"w1", "default"}, {"w2", "other"}, {"w3", "default"},
	} {
		if err := store.CreateWorkflow(ctx, &Workflow{ID: w.id, Name: w.id, ProfileID: w.profile}); err != nil {
			t.Fatalf("create %s: %v", w.id, err)
		}
	}
	listed, err := store.ListWorkflows(ctx, "default")
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 workflows for profile default, got %d", len(listed))
	}
}

// ListWorkflows leaves Nodes unpopulated so listing stays cheap, which left
// the GUI's workflow list with nothing to count: it showed "0 nodes" for
// every workflow, including a freshly created two-node one.
func TestNodeCounts(t *testing.T) {
	s, _ := newMigratedStore(t)
	ctx := context.Background()

	for _, wf := range []struct {
		id, profile string
		nodes       int
	}{
		{"two-nodes", "p1", 2},
		{"empty", "p1", 0},
		{"other-profile", "p2", 3},
	} {
		if err := s.CreateWorkflow(ctx, &Workflow{ID: wf.id, Name: wf.id, ProfileID: wf.profile}); err != nil {
			t.Fatalf("create %s: %v", wf.id, err)
		}
		nodes := make([]WorkflowNode, 0, wf.nodes)
		for i := 0; i < wf.nodes; i++ {
			nodes = append(nodes, testNode(fmt.Sprintf("%s-n%d", wf.id, i), "trigger.manual"))
		}
		if len(nodes) > 0 {
			if err := s.SaveWorkflowNodes(ctx, wf.id, nodes); err != nil {
				t.Fatalf("save nodes for %s: %v", wf.id, err)
			}
		}
	}

	counts, err := s.NodeCounts(ctx, "p1")
	if err != nil {
		t.Fatalf("NodeCounts: %v", err)
	}
	if counts["two-nodes"] != 2 {
		t.Errorf("two-nodes = %d, want 2", counts["two-nodes"])
	}
	// A workflow with no nodes must still be present as 0, not missing —
	// "0 nodes" is the right answer for it, unlike for the one above.
	if n, ok := counts["empty"]; !ok || n != 0 {
		t.Errorf("empty = %d (present %v), want 0 and present", n, ok)
	}
	if _, leaked := counts["other-profile"]; leaked {
		t.Error("another profile's workflow leaked into the counts")
	}

	// An empty profile is not "every profile": it is an error.
	if all, err := s.NodeCounts(ctx, ""); !errors.Is(err, ErrNoProfile) || all != nil {
		t.Errorf("NodeCounts(\"\") = %v, %v; want ErrNoProfile", all, err)
	}
	if _, err := s.ListWorkflows(ctx, ""); !errors.Is(err, ErrNoProfile) {
		t.Errorf("ListWorkflows(\"\") err = %v, want ErrNoProfile", err)
	}
	every, err := s.ListAllProfilesWorkflows(ctx)
	if err != nil {
		t.Fatalf("ListAllProfilesWorkflows: %v", err)
	}
	seen := map[string]bool{}
	for _, w := range every {
		seen[w.ID] = true
	}
	if !seen["other-profile"] || !seen["two-nodes"] {
		t.Errorf("ListAllProfilesWorkflows missed a profile's workflow: %v", seen)
	}
}
