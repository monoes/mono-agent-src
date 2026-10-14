package n8nimport_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// convertFixture converts a corpus fixture.
func convertFixture(t *testing.T, name string) *workflow.WorkflowFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/n8n/" + name)
	if err != nil {
		t.Fatal(err)
	}
	wf, _, err := n8nimport.Convert(raw)
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

// runNode executes one converted node the way the engine does: config
// templates resolved against the first item, then the node's executor.
func runNode(t *testing.T, wf *workflow.WorkflowFile, name string, items []map[string]interface{}) []map[string]interface{} {
	t.Helper()
	var node *workflow.WorkflowFileNode
	for i := range wf.Nodes {
		if wf.Nodes[i].Name == name {
			node = &wf.Nodes[i]
		}
	}
	if node == nil {
		t.Fatalf("no node %q", name)
	}
	in := make([]workflow.Item, len(items))
	for i, it := range items {
		in[i] = workflow.NewItem(it)
	}
	cfg := map[string]interface{}{}
	for k, v := range node.Config {
		cfg[k] = v
	}
	var first map[string]interface{}
	if len(in) > 0 {
		first = in[0].JSON
	}
	resolved, err := workflow.NewExpressionEngine().ResolveConfig(cfg, workflow.ExpressionContext{JSON: first})
	if err != nil {
		t.Fatalf("%s: resolve: %v", name, err)
	}
	factory, ok := noderegistry.Build(nil).Get(node.Type)
	if !ok {
		t.Fatalf("type %s not registered", node.Type)
	}
	outs, err := factory().Execute(context.Background(), workflow.NodeInput{Items: in}, resolved)
	if err != nil {
		t.Fatalf("%s: execute: %v", name, err)
	}
	var res []map[string]interface{}
	for _, o := range outs {
		for _, it := range o.Items {
			res = append(res, it.JSON)
		}
	}
	return res
}

// norm round-trips through JSON so numbers compare as float64.
func norm(v interface{}) interface{} {
	b, _ := json.Marshal(v)
	var out interface{}
	_ = json.Unmarshal(b, &out)
	return out
}

func expectJSON(t *testing.T, label string, got interface{}, want string) {
	t.Helper()
	var w interface{}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(norm(got), w) {
		g, _ := json.Marshal(got)
		t.Errorf("%s:\n got  %s\n want %s", label, g, want)
	}
}

func tagged(input int, items ...map[string]interface{}) []map[string]interface{} {
	for _, it := range items {
		it["__n8n_input"] = float64(input)
	}
	return items
}

func TestRuntimeMergeModes(t *testing.T) {
	wf := convertFixture(t, "merge_modes.json")
	in := func() []map[string]interface{} {
		return append(tagged(0, map[string]interface{}{"id": 1.0, "a": "x"}, map[string]interface{}{"id": 2.0, "a": "y"}),
			tagged(1, map[string]interface{}{"id": 2.0, "b": "z"}, map[string]interface{}{"id": 3.0, "b": "w"})...)
	}
	expectJSON(t, "outer join", runNode(t, wf, "Outer join", in()),
		`[{"id":1,"a":"x"},{"id":2,"a":"y","b":"z"},{"id":3,"b":"w"}]`)
	expectJSON(t, "by position", runNode(t, wf, "By position", in()),
		`[{"id_1":1,"a":"x","id_2":2,"b":"z"},{"id_1":2,"a":"y","id_2":3,"b":"w"}]`)
	expectJSON(t, "choose input 2", runNode(t, wf, "Choose", in()), `[{"id":2,"b":"z"},{"id":3,"b":"w"}]`)
	if got := runNode(t, wf, "Cross", in()); len(got) != 4 {
		t.Errorf("cross product: %d items, want 4", len(got))
	}
}

func TestRuntimeCompareDatasets(t *testing.T) {
	wf := convertFixture(t, "compare_datasets.json")
	in := append(tagged(0, map[string]interface{}{"id": 1.0, "plan": "pro"}, map[string]interface{}{"id": 2.0, "plan": "free"}, map[string]interface{}{"id": 3.0, "plan": "pro"}),
		tagged(1, map[string]interface{}{"id": 1.0, "plan": "pro"}, map[string]interface{}{"id": 2.0, "plan": "pro"}, map[string]interface{}{"id": 4.0, "plan": "free"})...)
	got := runNode(t, wf, "Compare Datasets (compare)", in)
	routes := map[string]int{}
	for _, it := range got {
		routes[it["__n8n_output"].(string)]++
	}
	expectJSON(t, "routes", routes, `{"0":1,"1":1,"2":1,"3":1}`)
}

func TestRuntimeTransforms(t *testing.T) {
	wf := convertFixture(t, "transform.json")
	orders := []map[string]interface{}{
		{"order": 1.0, "customer": "ada", "total": 30.0, "lines": []interface{}{map[string]interface{}{"sku": "A", "qty": 2.0}, map[string]interface{}{"sku": "B", "qty": 1.0}}},
		{"order": 2.0, "customer": "bob", "total": 10.0, "lines": []interface{}{map[string]interface{}{"sku": "A", "qty": 1.0}}},
	}
	sorted := runNode(t, wf, "Sort", orders)
	if sorted[0]["order"] != 1.0 {
		t.Errorf("sort by total desc: first = %v", sorted[0])
	}
	split := runNode(t, wf, "Split lines", orders)
	// As n8n: with selected other fields an object element stays under the
	// split field's name (only "no other fields" merges it into the item).
	expectJSON(t, "split out", split, `[{"order":1,"customer":"ada","lines":{"sku":"A","qty":2}},{"order":1,"customer":"ada","lines":{"sku":"B","qty":1}},{"order":2,"customer":"bob","lines":{"sku":"A","qty":1}}]`)
	lines := []map[string]interface{}{
		{"order": 1.0, "sku": "A", "qty": 2.0}, {"order": 1.0, "sku": "B", "qty": 1.0}, {"order": 2.0, "sku": "A", "qty": 1.0},
	}
	renamed := runNode(t, wf, "Rename", lines)
	sum := runNode(t, wf, "Summarize", renamed)
	expectJSON(t, "summarize", sum, `[{"sku":"A","sum_quantity":3,"unique_count_order":2},{"sku":"B","sum_quantity":1,"unique_count_order":1}]`)
	expectJSON(t, "limit last", runNode(t, wf, "Last", sum), `[{"sku":"B","sum_quantity":1,"unique_count_order":1}]`)
	expectJSON(t, "aggregate all", runNode(t, wf, "All rows", sum), `[{"rows":[{"sku":"A","sum_quantity":3},{"sku":"B","sum_quantity":1}]}]`)
}

func TestRuntimeSetAndCode(t *testing.T) {
	wf := convertFixture(t, "unmapped.json")
	got := runNode(t, wf, "JS fields", []map[string]interface{}{{"items": []interface{}{
		map[string]interface{}{"sku": "a", "qty": 9.0}, map[string]interface{}{"sku": "b", "qty": 1.0}}}})
	if g := norm(got[0]).(map[string]interface{}); g["skus"] != "a, b" || g["big"] != 1.0 || g["items"] == nil {
		t.Errorf("JS Set fields: %v", got[0])
	}
	shops := convertFixture(t, "schedules_and_shops.json")
	stamped := runNode(t, shops, "Stamp", []map[string]interface{}{{"report": "daily"}})
	if s, _ := stamped[0]["stamp"].(string); len(s) != 10 || stamped[0]["report"] != "daily" {
		t.Errorf("per-item code: %v", stamped[0])
	}
	rendered := runNode(t, shops, "Raw JSON (render JSON)", []map[string]interface{}{{"x": 1.0}})
	raw := runNode(t, shops, "Raw JSON", rendered)
	if raw[0]["report"] != "daily" || raw[0]["runAt"] == "" || raw[0]["x"] != nil || raw[0]["__n8n_raw"] != nil {
		t.Errorf("raw JSON set: %v", raw[0])
	}
	loop := convertFixture(t, "loop_batches.json")
	gen := runNode(t, loop, "Generate", []map[string]interface{}{{}})
	if len(gen) != 5 || gen[4]["email"] != "user5@example.com" {
		t.Errorf("code node items: %v", gen)
	}
	legacy := convertFixture(t, "legacy_v1.json")
	fi := runNode(t, legacy, "FunctionItem", []map[string]interface{}{{"name": "ab"}})
	expectJSON(t, "function item", fi, `[{"name":"ab","upper":"AB"}]`)
}

func TestRuntimeConditionsAndWebhook(t *testing.T) {
	wf := convertFixture(t, "llm_chains.json")
	got := runNode(t, wf, "Urgent? (evaluate)", []map[string]interface{}{{"text": "urgent: down"}, {"text": "fyi"}})
	if got[0]["__n8n_if"] != true || got[1]["__n8n_if"] != false {
		t.Errorf("regex condition: %v", got)
	}
	hook := convertFixture(t, "webhook_to_sheets.json")
	// The webhook item already carries the body under "body", as in n8n.
	if n := node(t, hook, "Webhook"); n.Type != "trigger.webhook" {
		t.Errorf("webhook type %s", n.Type)
	}
	norm := runNode(t, hook, "Normalize", []map[string]interface{}{{"body": map[string]interface{}{"email": "A@B.C ", "first_name": "Ada", "last_name": "L", "budget": "12"}}})
	if norm[0]["email"] != "a@b.c" || norm[0]["name"] != "Ada L" || norm[0]["budget"] != 12.0 {
		t.Errorf("normalize: %v", norm[0])
	}
}
