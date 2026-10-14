package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

// stubSchemas resolves node types to schemas without touching the embedded
// files, so a test's expectations don't move when a shipped schema does.
func stubSchemas(byType map[string]*NodeSchema) SchemaLookup {
	return func(nodeType string) (*NodeSchema, error) {
		if s, ok := byType[nodeType]; ok {
			return s, nil
		}
		return &NodeSchema{Fields: []NodeSchemaField{}}, nil
	}
}

func node(name, nodeType string, config map[string]interface{}) WorkflowNode {
	return WorkflowNode{Name: name, Type: nodeType, Config: config}
}

// The workflow that prompted this: its Generate Images node reads
// {{ json $json.prompts }}, nothing said so, and a run with no --input
// finished green having generated nothing.
func TestTriggerInputsFindsWhatAWorkflowReads(t *testing.T) {
	nodes := []WorkflowNode{
		node("Manual Trigger", "trigger.manual", map[string]interface{}{}),
		node("Generate Images", "gemini.chat_session_many", map[string]interface{}{
			"prompts": "{{ json $json.prompts }}",
			"mode":    "image",
		}),
	}
	schemas := stubSchemas(map[string]*NodeSchema{
		"gemini.chat_session_many": {Fields: []NodeSchemaField{
			{Key: "prompts", Examples: []string{"generate an image that: a wizard", "generate an image that: a dragon"}},
			{Key: "mode"},
		}},
	})

	fields := TriggerInputs(nodes, nil, schemas)

	if len(fields) != 1 {
		t.Fatalf("want one field, got %+v", fields)
	}
	got := fields[0]
	if got.Name != "prompts" {
		t.Errorf("name = %q", got.Name)
	}
	if !got.Structured {
		t.Error("read through the json function, so it wants a collection")
	}
	if len(got.Examples) != 2 {
		t.Errorf("examples should come from the reading node's schema, got %v", got.Examples)
	}
	if !reflect.DeepEqual(got.Nodes, []string{"Generate Images"}) {
		t.Errorf("nodes = %v", got.Nodes)
	}
}

func TestTriggerInputsShapesAndReferences(t *testing.T) {
	nodes := []WorkflowNode{
		node("A", "x.y", map[string]interface{}{
			"subject": "Re: {{ $json.topic }}",
			"rows":    "{{ json $json.records }}",
			"headers": map[string]interface{}{"auth": `{{ $json["apiKey"] }}`},
			"list":    []interface{}{"{{ $json.first }}"},
			// A node reference is not a trigger reference.
			"total": `{{ $node["Fetch"].json.count }}`,
		}),
	}

	fields := TriggerInputs(nodes, nil, stubSchemas(nil))

	shape := map[string]bool{}
	for _, f := range fields {
		shape[f.Name] = f.Structured
	}
	want := map[string]bool{"topic": false, "records": true, "apiKey": false, "first": false}
	if !reflect.DeepEqual(shape, want) {
		t.Fatalf("fields = %+v, want %v", shape, want)
	}
}

func TestTriggerInputsEmptyCases(t *testing.T) {
	if got := TriggerInputs(nil, nil, stubSchemas(nil)); len(got) != 0 {
		t.Errorf("no nodes: %+v", got)
	}
	plain := []WorkflowNode{node("A", "http.request", map[string]interface{}{"url": "https://example.com"})}
	if got := TriggerInputs(plain, nil, stubSchemas(nil)); len(got) != 0 {
		t.Errorf("no trigger references: %+v", got)
	}
}

// Two nodes reading the same field agree on one entry, and a structured read
// anywhere wins — the payload has to satisfy the strictest reader.
func TestTriggerInputsMergesRepeatedReads(t *testing.T) {
	nodes := []WorkflowNode{
		node("First", "x.y", map[string]interface{}{"a": "{{ $json.items }}"}),
		node("Second", "x.y", map[string]interface{}{"a": "{{ json $json.items }}"}),
	}
	fields := TriggerInputs(nodes, nil, stubSchemas(nil))
	if len(fields) != 1 {
		t.Fatalf("want one merged field, got %+v", fields)
	}
	if !fields[0].Structured {
		t.Error("a structured read anywhere makes the field structured")
	}
	if !reflect.DeepEqual(fields[0].Nodes, []string{"First", "Second"}) {
		t.Errorf("both readers should be named, got %v", fields[0].Nodes)
	}
}

// The skeleton is what a person edits, so it should show the examples rather
// than an empty box.
func TestTriggerInputSkeleton(t *testing.T) {
	fields := []TriggerField{
		{Name: "prompts", Structured: true, Examples: []string{"one", "two"}},
		{Name: "rows", Structured: true},
		{Name: "topic", Examples: []string{"a red bicycle"}},
		{Name: "note"},
	}

	raw, err := json.Marshal(TriggerInputSkeleton(fields))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"note":"","prompts":["one","two"],"rows":[],"topic":"a red bicycle"}`
	if string(raw) != want {
		t.Fatalf("skeleton =\n %s\nwant\n %s", raw, want)
	}
}

// Output feeds a dialog and a copy-pasteable command line, so it must not
// reshuffle between identical runs.
func TestTriggerInputsIsDeterministic(t *testing.T) {
	nodes := []WorkflowNode{
		node("A", "x.y", map[string]interface{}{
			"zeta": "{{ $json.z }}", "alpha": "{{ $json.a }}", "mid": "{{ $json.m }}",
		}),
	}
	first := TriggerInputs(nodes, nil, stubSchemas(nil))
	for i := 0; i < 20; i++ {
		if got := TriggerInputs(nodes, nil, stubSchemas(nil)); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n got %+v\nwant %+v", i, got, first)
		}
	}
}

// The shipped Gemini schema is what the GUI and CLI actually offer, so the
// real lookup has to reach it.
func TestTriggerInputsUsesTheShippedSchema(t *testing.T) {
	nodes := []WorkflowNode{
		node("Generate Images", "gemini.chat_session_many", map[string]interface{}{
			"prompts": "{{ json $json.prompts }}",
		}),
	}
	fields := TriggerInputs(nodes, nil, LoadDefaultSchema)
	if len(fields) != 1 || len(fields[0].Examples) == 0 {
		t.Fatalf("expected examples from the shipped schema, got %+v", fields)
	}
}

// QA Q2: the hello template saved as a workflow (manual → http.request →
// core.set reading $json.body) asked for a "body" trigger field. $json in a
// node the trigger does not feed is the previous node's output.
func TestTriggerInputsOnlyCountsNodesTheTriggerFeeds(t *testing.T) {
	tmpl, ok := GetTemplate("hello")
	if !ok {
		t.Fatal("hello template not bundled")
	}
	wf, err := ParseWorkflowFileBytes(mustJSON(t, tmpl))
	if err != nil {
		t.Fatal(err)
	}
	if got := TriggerInputs(wf.Nodes, wf.Connections, stubSchemas(nil)); len(got) != 0 {
		t.Errorf("hello reads no trigger input, got %+v", got)
	}

	nodes := []WorkflowNode{
		{ID: "t", Name: "Start", Type: "trigger.manual"},
		{ID: "a", Name: "Ask", Type: "x.y", Config: map[string]interface{}{"q": "{{ $json.prompt }}"}},
		{ID: "b", Name: "Use", Type: "x.y", Config: map[string]interface{}{
			"r": "{{ $json.answer }}", "s": `{{ $node["Start"].json.style }}`,
		}},
	}
	conns := []WorkflowConnection{
		{SourceNodeID: "t", TargetNodeID: "a"},
		{SourceNodeID: "a", TargetNodeID: "b"},
	}
	var names []string
	for _, f := range TriggerInputs(nodes, conns, stubSchemas(nil)) {
		names = append(names, f.Name)
	}
	if !reflect.DeepEqual(names, []string{"prompt", "style"}) {
		t.Errorf("fields = %v, want [prompt style]", names)
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
