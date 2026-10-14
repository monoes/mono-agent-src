package workflow

import "testing"

func TestTriggerInputsWorkflowFollowsTheFlow(t *testing.T) {
	wf := &Workflow{Nodes: []WorkflowNode{
		{ID: "t", Type: "trigger.manual", Name: "Start"},
		{ID: "c", Type: "core.code", Name: "Make", Config: map[string]interface{}{"code": "return [{a: 1}]"}},
		{ID: "f", Type: "core.filter", Name: "Big", Config: map[string]interface{}{"condition": "{{ gt $json.a 4 }}"}},
		{ID: "l", Type: "core.limit", Name: "Few", Config: map[string]interface{}{"max_items": "{{ $json.max }}"}},
		{ID: "h", Type: "http.request", Name: "Get", Config: map[string]interface{}{"url": "{{ $json.url }}"}},
		{ID: "s", Type: "core.set", Name: "Pick", Config: map[string]interface{}{"assignments": []interface{}{
			map[string]interface{}{"field": "code", "value": "{{ $json.status }}"}}}},
	}, Connections: []WorkflowConnection{
		{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"},
		{SourceNodeID: "c", SourceHandle: "main", TargetNodeID: "f", TargetHandle: "main"},
		{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "l", TargetHandle: "main"},
		{SourceNodeID: "l", SourceHandle: "main", TargetNodeID: "h", TargetHandle: "main"},
		{SourceNodeID: "h", SourceHandle: "main", TargetNodeID: "s", TargetHandle: "main"},
	}}
	got := map[string]bool{}
	for _, f := range TriggerInputsWorkflow(wf, nil) {
		got[f.Name] = true
	}
	// max: read by limit, straight from the trigger. url: through limit, a
	// passthrough. a: made by the Code node. status: made by the HTTP request.
	for _, want := range []string{"max", "url"} {
		if !got[want] {
			t.Errorf("%s should be a trigger input; got %v", want, got)
		}
	}
	for _, not := range []string{"a", "status"} {
		if got[not] {
			t.Errorf("%s is produced upstream, not a trigger input; got %v", not, got)
		}
	}
	// Without connections every read counts, as before.
	wf.Connections = nil
	if n := len(TriggerInputsWorkflow(wf, nil)); n != 4 {
		t.Errorf("no connections: %d fields, want 4", n)
	}
}
