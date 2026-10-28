package workflow

import (
	"context"
	"testing"
)

func newHybridForTest(t *testing.T) (*HybridWorkflowStore, *SQLiteWorkflowStore) {
	t.Helper()
	files, err := NewWorkflowFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sqlStore, _ := newMigratedStore(t)
	return NewHybridWorkflowStore(files, sqlStore), sqlStore
}

func mirrorWF(id string, nodeIDs ...string) *Workflow {
	w := &Workflow{ProfileID: "default", ID: id, Name: id, Version: 3}
	for i, n := range nodeIDs {
		typ := "core.set"
		if i == 0 {
			typ = "trigger.manual"
		}
		w.Nodes = append(w.Nodes, WorkflowNode{ID: n, WorkflowID: id, Type: typ, Name: n, Config: map[string]interface{}{"k": "v"}})
	}
	if len(nodeIDs) > 1 {
		w.Connections = []WorkflowConnection{{ID: id + "-c1", WorkflowID: id, SourceNodeID: nodeIDs[0], SourceHandle: "main",
			TargetNodeID: nodeIDs[1], TargetHandle: "main"}}
	}
	return w
}

// TestHybridSaveWorkflowMirrorsToSQLite: a file-store save (the desktop
// editor's path) also writes SQLite's row, nodes and connections.
func TestHybridSaveWorkflowMirrorsToSQLite(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	w := mirrorWF("wf-m", "m-trig", "m-set")
	if err := h.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	// The save numbers the definition (#21): a new workflow's first
	// recorded version is 1, whatever the document carried.
	if w.Version != 1 {
		t.Errorf("version = %d, want 1", w.Version)
	}
	got, err := s.GetWorkflow(ctx, "wf-m")
	if err != nil || got == nil || len(got.Nodes) != 2 || len(got.Connections) != 1 {
		t.Fatalf("SQLite copy = %+v, %v", got, err)
	}
	types := map[string]string{}
	for _, n := range got.Nodes {
		types[n.ID] = n.Type
	}
	if types["m-trig"] != "trigger.manual" || types["m-set"] != "core.set" {
		t.Errorf("types = %v", types)
	}

	// An edit removing a node and renaming the workflow follows.
	w.Nodes, w.Connections, w.Name = w.Nodes[:1], nil, "renamed"
	if err := h.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetWorkflow(ctx, "wf-m")
	if len(got.Nodes) != 1 || len(got.Connections) != 0 || got.Name != "renamed" {
		t.Errorf("after edit: %d nodes, %d conns, name %q", len(got.Nodes), len(got.Connections), got.Name)
	}
}

// TestHybridMirrorKeepsProfile: updating an existing row leaves its
// profile_id alone (the desktop app tags it after the first save).
func TestHybridMirrorKeepsProfile(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	w := mirrorWF("wf-p", "p-trig")
	if err := h.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO profiles (id, name) VALUES ('work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE workflows SET profile_id = 'work' WHERE id = 'wf-p'`); err != nil {
		t.Fatal(err)
	}
	approveForTest(t, h, "wf-p")
	if err := h.SetWorkflowActive(ctx, "wf-p", true); err != nil {
		t.Fatal(err)
	}
	var profile string
	var active int
	if err := s.db.QueryRow(`SELECT profile_id, is_active FROM workflows WHERE id = 'wf-p'`).Scan(&profile, &active); err != nil {
		t.Fatal(err)
	}
	if profile != "work" || active != 1 {
		t.Errorf("profile %q active %d", profile, active)
	}
}

// TestHybridMirrorSkipsSharedIDs: a workflow reusing another workflow's
// node ids is saved to the file store but not mirrored, and the other
// workflow keeps its nodes.
func TestHybridMirrorSkipsSharedIDs(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	if err := h.SaveWorkflow(ctx, mirrorWF("wf-a", "shared-trig", "a-set")); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveWorkflow(ctx, mirrorWF("wf-b", "shared-trig")); err != nil {
		t.Fatalf("save must still succeed: %v", err)
	}
	var owner string
	if err := s.db.QueryRow(`SELECT workflow_id FROM workflow_nodes WHERE id = 'shared-trig'`).Scan(&owner); err != nil || owner != "wf-a" {
		t.Errorf("shared node owner = %q (%v)", owner, err)
	}
	if b, _ := h.GetWorkflow(ctx, "wf-b"); b == nil || len(b.Nodes) != 1 {
		t.Errorf("file copy of wf-b = %+v", b)
	}
}
