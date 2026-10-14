package control

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestRemoveDuplicates_IncrementalKey(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	cfg := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions",
		"logic": "removeItemsUpToStoredIncrementalKey", "dedupeValue": "id"}
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems(3, 1, 2), cfg)); got != "[3 1 2]" {
		t.Fatalf("first run = %s, want every item", got)
	}
	// Only values above the stored maximum (3) pass, as numbers ("10" > 9 > 3).
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems(2, 3, "10", 9, 4.5), cfg)); got != "[10 9 4.5]" {
		t.Fatalf("second run = %s, want [10 9 4.5]", got)
	}
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems(9, 11), cfg)); got != "[11]" {
		t.Fatalf("third run = %s, want [11] (the max is now 10)", got)
	}
	// Workflow scope keeps its own maximum, shared by every node.
	wcfg := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions",
		"logic": "removeItemsUpToStoredIncrementalKey", "dedupeValue": "{{ $json.id }}", "scope": "workflow"}
	runDedupe(t, ctx, "a", dedupeItems(5), wcfg)
	if got := dedupeIDs(t, runDedupe(t, ctx, "b", dedupeItems(5, 6), wcfg)); got != "[6]" {
		t.Fatalf("workflow scope = %s, want [6]", got)
	}
	// Clearing the history forgets the maximum too.
	runDedupe(t, ctx, "n1", nil, map[string]interface{}{"operation": "clearDeduplicationHistory"})
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems(1), cfg)); got != "[1]" {
		t.Fatalf("after clear = %s, want [1]", got)
	}
}

func TestRemoveDuplicates_StoredDate(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	cfg := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions",
		"logic": "removeItemsUpToStoredDate", "dedupeValue": "id"}
	if got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems("2026-10-13T09:00:00Z", "2026-10-12"), cfg)); got != "[2026-10-13T09:00:00Z 2026-10-12]" {
		t.Fatalf("first run = %s", got)
	}
	got := dedupeIDs(t, runDedupe(t, ctx, "n1", dedupeItems(
		"2026-10-13T09:00:00Z",          // the stored date itself: not later
		"2026-10-13T11:00:00+01:00",     // 10:00 UTC
		"Wed, 14 Oct 2026 09:00:00 GMT", // RFC1123
		"2026-10-01",
	), cfg))
	if got != "[2026-10-13T11:00:00+01:00 Wed, 14 Oct 2026 09:00:00 GMT]" {
		t.Fatalf("second run = %s", got)
	}
}

func TestRemoveDuplicates_WatermarkErrors(t *testing.T) {
	ctx := vault.ContextWithDB(context.Background(), newDedupeDB(t))
	n := &RemoveDuplicatesNode{}
	for name, c := range map[string]struct {
		items []workflow.Item
		cfg   map[string]interface{}
	}{
		"not a number": {dedupeItems("abc"), map[string]interface{}{"logic": "removeItemsUpToStoredIncrementalKey"}},
		"not a date":   {dedupeItems("soon"), map[string]interface{}{"logic": "removeItemsUpToStoredDate"}},
		"bad logic":    {dedupeItems(1), map[string]interface{}{"logic": "nope"}},
	} {
		c.cfg["operation"] = "removeItemsRepeatedWithinPreviousExecutions"
		c.cfg["dedupeValue"] = "id"
		if _, err := n.Execute(ctx, workflow.NodeInput{Items: c.items, WorkflowID: "wf1", NodeID: "n1"}, c.cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
