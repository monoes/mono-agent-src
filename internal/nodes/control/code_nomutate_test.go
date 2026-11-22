package control

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestCodeNode_DoesNotMutateUpstreamNestedData(t *testing.T) {
	nested := map[string]interface{}{"x": float64(0)}
	list := []interface{}{float64(1)}
	in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{"nested": nested, "list": list}}}}
	n := &CodeNode{}
	_, err := n.Execute(context.Background(), in, map[string]interface{}{
		"code": `$json.nested.x = 1; $input.all()[0].nested.x = 2; $json.list.push(5); return [{a:1}];`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nested["x"] != float64(0) || len(list) != 1 {
		t.Fatalf("upstream mutated: %v %v", nested, list)
	}
}
