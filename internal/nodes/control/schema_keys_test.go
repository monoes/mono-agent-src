package control

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestSwitchAcceptsSchemaCasesShape is a regression test: core.switch.json's
// "cases" field is a flat array of strings (each string doubling as both the
// match value and the output handle), and the config key is "field" not
// "expression". Previously the code required cases as []map{value,handle} and
// read "expression", so the switch node always routed everything to "default".
func TestSwitchAcceptsSchemaCasesShape(t *testing.T) {
	node := &SwitchNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{
			{JSON: map[string]interface{}{"status": "pending"}},
			{JSON: map[string]interface{}{"status": "done"}},
			{JSON: map[string]interface{}{"status": "unknown"}},
		},
	}, map[string]interface{}{
		"field": "{{$json.status}}",
		"cases": []interface{}{"pending", "done"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	byHandle := make(map[string]int)
	for _, o := range out {
		byHandle[o.Handle] = len(o.Items)
	}
	if byHandle["pending"] != 1 {
		t.Errorf("handle 'pending' got %d items, want 1", byHandle["pending"])
	}
	if byHandle["done"] != 1 {
		t.Errorf("handle 'done' got %d items, want 1", byHandle["done"])
	}
	if byHandle["default"] != 1 {
		t.Errorf("handle 'default' got %d items, want 1", byHandle["default"])
	}
}

// TestSortUsesDirectionKey is a regression test: core.sort.json's field is
// "direction", not "order".
func TestSortUsesDirectionKey(t *testing.T) {
	node := &SortNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{
			{JSON: map[string]interface{}{"n": "1"}},
			{JSON: map[string]interface{}{"n": "3"}},
			{JSON: map[string]interface{}{"n": "2"}},
		},
	}, map[string]interface{}{"field": "n", "direction": "desc"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	items := out[0].Items
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0].JSON["n"] != "3" || items[2].JSON["n"] != "1" {
		t.Errorf("descending sort not applied: got order %v, %v, %v", items[0].JSON["n"], items[1].JSON["n"], items[2].JSON["n"])
	}
}

// TestSortDescPreservesTieOrder is a regression test: descending sort must
// keep the input order of items with equal keys. Previously desc was `return
// !less`, which reported both i<j and j<i for equal keys, violating strict
// weak ordering and causing sort.SliceStable to reorder tied runs.
func TestSortDescPreservesTieOrder(t *testing.T) {
	node := &SortNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{
			{JSON: map[string]interface{}{"k": 1.0, "id": "a"}},
			{JSON: map[string]interface{}{"k": 1.0, "id": "b"}},
			{JSON: map[string]interface{}{"k": 2.0, "id": "c"}},
		},
	}, map[string]interface{}{"field": "k", "type": "number", "direction": "desc"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	items := out[0].Items
	got := []string{
		items[0].JSON["id"].(string),
		items[1].JSON["id"].(string),
		items[2].JSON["id"].(string),
	}
	want := []string{"c", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("desc tie order = %v, want %v", got, want)
		}
	}
}

// TestAggregateAcceptsFlatSchemaShape is a regression test: core.aggregate.json
// exposes flat "operation"/"field"/"output_field" fields, but the code
// previously required an "operations" array of objects that the schema never
// produced, so the node could never be driven from the UI.
func TestAggregateAcceptsFlatSchemaShape(t *testing.T) {
	node := &AggregateNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{
			{JSON: map[string]interface{}{"amount": 10.0}},
			{JSON: map[string]interface{}{"amount": 5.0}},
		},
	}, map[string]interface{}{"operation": "sum", "field": "amount"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out[0].Items) != 1 {
		t.Fatalf("got %d result items, want 1", len(out[0].Items))
	}
	if got := out[0].Items[0].JSON["sum"]; got != 15.0 {
		t.Errorf("sum = %v, want 15.0 (also checks default output_field == operation name)", got)
	}
}

// QA Q5: an aggregate with neither "operation" nor "operations" passed
// `workflow validate` (the schema's operation defaults to count) and then
// failed at run time. The run now applies the schema default.
func TestAggregateDefaultsToCount(t *testing.T) {
	out, err := (&AggregateNode{}).Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{}}, {JSON: map[string]interface{}{}}, {JSON: map[string]interface{}{}}},
	}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := out[0].Items[0].JSON["count"]; got != 3 && got != 3.0 {
		t.Errorf("count = %v (%T), want 3", got, got)
	}
}
