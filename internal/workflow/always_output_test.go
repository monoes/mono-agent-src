package workflow

import "testing"

func TestAlwaysOutputData(t *testing.T) {
	on := map[string]interface{}{"always_output_data": true}
	// Nothing emitted: one empty item on the first non-error output.
	out := alwaysOutputData(on, []NodeOutput{{Handle: "error"}, {Handle: "true"}, {Handle: "false"}})
	if len(out[1].Items) != 1 || len(out[0].Items) != 0 || len(out[2].Items) != 0 {
		t.Fatalf("empty outputs: %+v", out)
	}
	if out := alwaysOutputData(on, nil); len(out) != 1 || out[0].Handle != "main" || len(out[0].Items) != 1 {
		t.Fatalf("no outputs: %+v", out)
	}
	// Items somewhere: unchanged.
	in := []NodeOutput{{Handle: "true"}, {Handle: "false", Items: []Item{NewItem(nil)}}}
	if out := alwaysOutputData(on, in); len(out[0].Items) != 0 {
		t.Fatalf("items present must not add one: %+v", out)
	}
	// Off (the default) and n8n's own key.
	if out := alwaysOutputData(map[string]interface{}{}, nil); len(out) != 0 {
		t.Fatalf("off: %+v", out)
	}
	if out := alwaysOutputData(map[string]interface{}{"alwaysOutputData": true}, nil); len(out) != 1 {
		t.Fatalf("n8n key: %+v", out)
	}
	// The error output alone does not count as output.
	if out := alwaysOutputData(on, []NodeOutput{{Handle: "error", Items: []Item{NewItem(nil)}}}); len(out) != 2 || len(out[1].Items) != 1 {
		t.Fatalf("error only: %+v", out)
	}
}
