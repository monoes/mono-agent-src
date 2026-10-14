package control

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// n8n code reads the input as `items`; code that declares its own `items`
// keeps working.
func TestCodeItemsGlobal(t *testing.T) {
	in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{"n": 1.0}}, {JSON: map[string]interface{}{"n": 2.0}}}}
	for name, code := range map[string]string{
		"items":           "return items.map(i => ({json: {d: i.json.n * 2}}));",
		"own let items":   "let items = [1]; return [{json: {d: items.length}}];",
		"own var items":   "var items = [1, 2, 3]; return [{json: {d: items.length}}];",
		"top-level await": "await Promise.resolve(); return items.map(i => ({json: {d: i.json.n}}));",
	} {
		out, err := (&CodeNode{}).Execute(context.Background(), in, map[string]interface{}{"code": code})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(out) == 0 || len(out[0].Items) == 0 {
			t.Fatalf("%s: no output", name)
		}
	}
	out, _ := (&CodeNode{}).Execute(context.Background(), in, map[string]interface{}{"code": "return items.map(i => ({json: {d: i.json.n * 2}}));"})
	if out[0].Items[1].JSON["d"] != 4.0 && out[0].Items[1].JSON["d"] != int64(4) {
		t.Errorf("items value: %v", out[0].Items[1].JSON)
	}
}
