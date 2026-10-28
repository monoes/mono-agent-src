package workflow

import (
	"context"
	"testing"
)

// sqlOnlyHybrid is a hybrid store with no file store: definitions live in
// SQLite alone.
func sqlOnlyHybrid(t *testing.T) *HybridWorkflowStore {
	t.Helper()
	sqlStore, _ := newMigratedStore(t)
	return NewHybridWorkflowStore(nil, sqlStore)
}

func TestHybridCRUD_SQLiteOnly(t *testing.T) {
	ctx := context.Background()
	h := sqlOnlyHybrid(t)

	var saved []string
	h.SetOnSaved(func(_ context.Context, id string) { saved = append(saved, id) })

	w := mirrorWF("wf-sql", "s-trig")
	if err := h.CreateWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := h.GetWorkflow(ctx, "wf-sql")
	if err != nil || got == nil || got.Name != "wf-sql" {
		t.Fatalf("GetWorkflow = %+v, %v", got, err)
	}
	w.Name = "renamed"
	if err := h.UpdateWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, _ = h.GetWorkflow(ctx, "wf-sql")
	if got.Name != "renamed" {
		t.Errorf("name after update = %q", got.Name)
	}
	if len(saved) != 3 {
		t.Errorf("OnSaved ran %d times, want 3 (%v)", len(saved), saved)
	}

	list, err := h.ListWorkflows(ctx, "default")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListWorkflows = %d, %v", len(list), err)
	}

	approveForTest(t, h, "wf-sql")
	if err := h.SetWorkflowActive(ctx, "wf-sql", true); err != nil {
		t.Fatal(err)
	}
	if got, _ = h.GetWorkflow(ctx, "wf-sql"); !got.IsActive {
		t.Error("SetWorkflowActive did not reach SQLite")
	}
	if err := h.DeleteWorkflow(ctx, "wf-sql"); err != nil {
		t.Fatal(err)
	}
	if got, _ = h.GetWorkflow(ctx, "wf-sql"); got != nil {
		t.Errorf("workflow still present after delete: %+v", got)
	}
}

// TestHybridListMergesWithoutDuplicates: a workflow saved through the hybrid
// store exists in both backends and is listed once; one that only SQLite
// holds is still listed.
func TestHybridListMergesWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)

	if err := h.CreateWorkflow(ctx, mirrorWF("wf-both", "b-trig")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWorkflow(ctx, mirrorWF("wf-legacy", "l-trig")); err != nil {
		t.Fatal(err)
	}

	list, err := h.ListWorkflows(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, wf := range list {
		seen[wf.ID]++
	}
	if len(list) != 2 || seen["wf-both"] != 1 || seen["wf-legacy"] != 1 {
		t.Errorf("listed %v", seen)
	}

	// The SQLite-only workflow resolves through GetWorkflow's fallback.
	if got, err := h.GetWorkflow(ctx, "wf-legacy"); err != nil || got == nil {
		t.Errorf("GetWorkflow(legacy) = %+v, %v", got, err)
	}
	if got, err := h.GetWorkflow(ctx, "nope"); err != nil || got != nil {
		t.Errorf("GetWorkflow(missing) = %+v, %v", got, err)
	}
}

// TestHybridSetWorkflowActive_FileCopy: with a file-store copy, the flag
// changes in the file and is mirrored to SQLite.
func TestHybridSetWorkflowActive_FileCopy(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	if err := h.CreateWorkflow(ctx, mirrorWF("wf-act", "a-trig")); err != nil {
		t.Fatal(err)
	}
	approveForTest(t, h, "wf-act")
	if err := h.SetWorkflowActive(ctx, "wf-act", true); err != nil {
		t.Fatal(err)
	}
	fromFile, _ := h.files.GetWorkflow(ctx, "wf-act")
	fromSQL, _ := s.GetWorkflow(ctx, "wf-act")
	if fromFile == nil || !fromFile.IsActive || fromSQL == nil || !fromSQL.IsActive {
		t.Errorf("active flag: file %+v, sql %+v", fromFile, fromSQL)
	}
}

// approveForTest records the workflow's current definition as the operator's, as `workflow
// activate` or a save from the app would, so a test of something else can switch it on.
func approveForTest(t *testing.T, store interface {
	ApproveDefinition(ctx context.Context, workflowID string) error
}, id string) {
	t.Helper()
	if err := store.ApproveDefinition(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}
