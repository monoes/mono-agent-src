package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func newDedupeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "dedupe.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	return db.DB
}

func dedupeItems(ids ...interface{}) []workflow.Item {
	items := make([]workflow.Item, len(ids))
	for i, id := range ids {
		items[i] = workflow.NewItem(map[string]interface{}{"id": id, "pos": i})
	}
	return items
}

func dedupeIDs(t *testing.T, out []workflow.NodeOutput) string {
	t.Helper()
	if len(out) != 1 || out[0].Handle != "main" {
		t.Fatalf("outputs = %+v, want one main output", out)
	}
	ids := make([]interface{}, len(out[0].Items))
	for i, it := range out[0].Items {
		ids[i] = it.JSON["id"]
	}
	return fmt.Sprint(ids)
}

func runDedupe(t *testing.T, ctx context.Context, node string, items []workflow.Item, cfg map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	out, err := (&RemoveDuplicatesNode{}).Execute(ctx, workflow.NodeInput{
		Items: items, WorkflowID: "wf1", ExecutionID: "e1", NodeID: node,
	}, cfg)
	if err != nil {
		t.Fatalf("Execute(%v): %v", cfg, err)
	}
	return out
}

func TestRemoveDuplicates_InputItems(t *testing.T) {
	ctx := context.Background()
	items := dedupeItems("a", "b", "a")
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", items, map[string]interface{}{"field": "id"})); got != "[a b]" {
		t.Errorf("default operation = %s, want [a b]", got)
	}
	cfg := map[string]interface{}{"operation": "removeDuplicateInputItems", "field": "id", "keep": "last"}
	out := runDedupe(t, ctx, "n1", items, cfg)
	if got := out[0].Items[0].JSON["pos"]; got != 2 {
		t.Errorf("keep=last kept pos %v for a, want 2", got)
	}
}

func TestRemoveDuplicates_AcrossExecutions(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	cfg := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "id"}

	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems("a", "b", "a"), cfg)); got != "[a b]" {
		t.Fatalf("first run = %s, want [a b]", got)
	}
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems("b", "c"), cfg)); got != "[c]" {
		t.Fatalf("second run = %s, want [c]", got)
	}
	// Node scope: another node keeps its own history.
	if got := dedupeIDs(t, runDedupe(t, ctx, "n2", dedupeItems("a"), cfg)); got != "[a]" {
		t.Fatalf("other node = %s, want [a]", got)
	}
}

func TestRemoveDuplicates_ExpressionAndWorkflowScope(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	cfg := map[string]interface{}{
		"operation":   "removeItemsRepeatedWithinPreviousExecutions",
		"dedupeValue": "{{ $json.id }}-x",
		"scope":       "workflow",
	}
	runDedupe(t, ctx, "n1", dedupeItems("a"), cfg)
	if got := dedupeIDs(t, runDedupe(t, ctx, "n2", dedupeItems("a", "b"), cfg)); got != "[b]" {
		t.Fatalf("workflow scope from another node = %s, want [b]", got)
	}
}

func TestRemoveDuplicates_HistorySize(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	cfg := map[string]interface{}{
		"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "id", "historySize": float64(1),
	}
	runDedupe(t, ctx, "n1", dedupeItems("a"), cfg)
	runDedupe(t, ctx, "n1", dedupeItems("b"), cfg)
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems("a"), cfg)); got != "[a]" {
		t.Fatalf("a evicted by historySize=1 = %s, want [a]", got)
	}
}

func TestRemoveDuplicates_ClearHistory(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	cfg := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "id"}
	runDedupe(t, ctx, "n1", dedupeItems("a"), cfg)

	clear := map[string]interface{}{"operation": "clearDeduplicationHistory"}
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems("x", "x"), clear)); got != "[x x]" {
		t.Fatalf("clear passes items through = %s, want [x x]", got)
	}
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems("a"), cfg)); got != "[a]" {
		t.Fatalf("after clear = %s, want [a]", got)
	}
}

func TestRemoveDuplicates_Errors(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	in := workflow.NodeInput{Items: dedupeItems("a"), WorkflowID: "wf1", NodeID: "n1"}
	n := &RemoveDuplicatesNode{}
	cases := map[string]map[string]interface{}{
		"unknown operation": {"operation": "nope"},
		"bad scope":         {"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "id", "scope": "global"},
		"missing value":     {"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "missing"},
	}
	for name, cfg := range cases {
		if _, err := n.Execute(ctx, in, cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// No database (e.g. a run outside the engine): say so instead of letting everything through.
	_, err := n.Execute(context.Background(), in, map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "id"})
	if !errors.Is(err, workflow.ErrNoWorkflowState) {
		t.Errorf("no db err = %v, want ErrNoWorkflowState", err)
	}
}
