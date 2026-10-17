package n8nimport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func convertString(t *testing.T, src string) (*workflow.WorkflowFile, *Report) {
	t.Helper()
	wf, rep, err := Convert([]byte(src))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return wf, rep
}

func nodeByName(wf *workflow.WorkflowFile, name string) *workflow.WorkflowFileNode {
	for i := range wf.Nodes {
		if wf.Nodes[i].Name == name {
			return &wf.Nodes[i]
		}
	}
	return nil
}

func TestAppMapperIsNearIdentity(t *testing.T) {
	RegisterApp(AppMapper{
		N8nTypes:           []string{"n8n-nodes-base.testTrello"},
		NodeType:           "app.trello",
		CredentialPlatform: map[string]string{"trelloApi": "trello"},
		DefaultResource:    "card", DefaultOperation: "create",
		ParamMap: map[string]map[string]string{"card.create": {"listId": "list_id"}},
	})
	wf, rep := convertString(t, `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"T","type":"n8n-nodes-base.testTrello","typeVersion":1,"position":[200,0],
	  "parameters":{"listId":"L1","name":"={{ $json.title }}","additionalFields":{"pos":"top"}},
	  "credentials":{"trelloApi":{"id":"1","name":"My Trello"}}}],
	 "connections":{"M":{"main":[[{"node":"T","type":"main","index":0}]]}}}`)
	n := nodeByName(wf, "T")
	if n.Type != "app.trello" {
		t.Fatalf("type = %s", n.Type)
	}
	want := map[string]interface{}{"resource": "card", "operation": "create", "list_id": "L1",
		"name": "{{ $json.title }}", "credential_id": "trello"}
	for k, v := range want {
		if n.Config[k] != v {
			t.Errorf("config[%s] = %v, want %v", k, n.Config[k], v)
		}
	}
	if add, _ := n.Config["additionalFields"].(map[string]interface{}); add["pos"] != "top" {
		t.Errorf("additionalFields = %v", n.Config["additionalFields"])
	}
	if len(rep.Credentials) != 1 || rep.Credentials[0].Platform != "trello" || rep.Credentials[0].N8nName != "My Trello" {
		t.Errorf("credentials = %+v", rep.Credentials)
	}
	if !rep.Complete || rep.Counts.Full != 2 {
		t.Errorf("report: %s", rep.Text())
	}
	_, app := SupportedTypes()
	if !contains(app, "n8n-nodes-base.testTrello") {
		t.Errorf("SupportedTypes app list lacks the package type: %v", app)
	}
}

func TestAppMapperCoversUnsupportedNativeOperation(t *testing.T) {
	withoutAppSources(t) // the catalog's github package would map it already
	src := `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"G","type":"n8n-nodes-base.github","typeVersion":1,"position":[200,0],
	  "parameters":{"resource":"file","operation":"create","owner":"o","repository":"r","filePath":"a.txt"}}],
	 "connections":{"M":{"main":[[{"node":"G","type":"main","index":0}]]}}}`
	_, rep := convertString(t, src)
	if rep.Complete {
		t.Fatalf("file.create should be unmapped without a package: %s", rep.Text())
	}
	RegisterApp(AppMapper{N8nTypes: []string{"n8n-nodes-base.github"}, Extends: "service.github",
		CredentialPlatform: map[string]string{"githubApi": "github"}})
	defer func() {
		regMu.Lock()
		delete(apps, "n8n-nodes-base.github")
		regMu.Unlock()
	}()
	wf, rep := convertString(t, src)
	n := nodeByName(wf, "G")
	if !rep.Complete || n.Type != "service.github" || n.Config["resource"] != "file" || n.Config["filePath"] != "a.txt" {
		t.Fatalf("extension fallback: %v %v\n%s", n.Type, n.Config, rep.Text())
	}
}

func TestUnknownTypeBecomesFailingStub(t *testing.T) {
	wf, rep := convertString(t, `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"X","type":"n8n-nodes-base.somethingNew","typeVersion":1,"position":[200,0],"parameters":{"q":"={{ $json.a }}"}}],
	 "connections":{"M":{"main":[[{"node":"X","type":"main","index":0}]]}}}`)
	n := nodeByName(wf, "X")
	if n.Type != "core.code" || !strings.Contains(n.Config["code"].(string), "not available in mono-agent yet") {
		t.Fatalf("stub = %v", n.Config)
	}
	got, err := workflow.NewExpressionEngine().ResolveConfig(n.Config, workflow.ExpressionContext{})
	if err != nil {
		t.Fatalf("stub config must resolve: %v", err)
	}
	if p := got["n8n_parameters"].(map[string]interface{}); p["q"] != "={{ $json.a }}" {
		t.Errorf("kept parameters = %v", p)
	}
	if rep.Complete || rep.Counts.Unmapped != 1 {
		t.Errorf("report: %s", rep.Text())
	}
}

func TestConditionTemplatesEvaluate(t *testing.T) {
	eng := workflow.NewExpressionEngine()
	item := map[string]interface{}{"name": "Ada Lovelace", "n": 7.0, "ok": true, "tags": []interface{}{"x", "y"}, "empty": ""}
	cases := []struct {
		c    cond
		cs   bool
		want string
	}{
		{cond{"={{ $json.name }}", "ada lovelace", "string", "equals"}, false, "true"},
		{cond{"={{ $json.name }}", "ada lovelace", "string", "equals"}, true, "false"},
		{cond{"={{ $json.name }}", "Love", "string", "contains"}, true, "true"},
		{cond{"={{ $json.name }}", "Ada", "string", "startsWith"}, true, "true"},
		{cond{"={{ $json.name }}", "lace", "string", "notEndsWith"}, true, "false"},
		{cond{"={{ $json.n }}", 5.0, "number", "gt"}, true, "true"},
		{cond{"={{ $json.n }}", "7", "number", "equals"}, true, "true"},
		{cond{"={{ $json.n }}", 7.0, "number", "lt"}, true, "false"},
		{cond{"={{ $json.ok }}", nil, "boolean", "true"}, true, "true"},
		{cond{"={{ $json.empty }}", nil, "string", "empty"}, true, "true"},
		{cond{"={{ $json.missing }}", nil, "string", "exists"}, true, "false"},
		{cond{"={{ $json.name }}", nil, "string", "notExists"}, true, "false"},
		{cond{"={{ $json.tags }}", 2.0, "array", "lengthEquals"}, true, "true"},
		{cond{"={{ $json.tags }}", "y", "array", "contains"}, true, "true"},
		{cond{"2026-01-02", "2026-01-01", "dateTime", "after"}, true, "true"},
	}
	for _, tc := range cases {
		c := newMapContext(&n8nNode{Name: "n", Parameters: map[string]interface{}{}}, "w")
		tmpl, err := c.condTemplate("c", condSet{conds: []cond{tc.c}, caseSensitive: tc.cs})
		if err != nil {
			t.Errorf("%+v: %v", tc.c, err)
			continue
		}
		got, err := eng.EvaluateString("{{ "+tmpl+" }}", workflow.ExpressionContext{JSON: item})
		if err != nil || got != tc.want {
			t.Errorf("%+v (cs %v) → %s = %q, %v; want %s", tc.c, tc.cs, tmpl, got, err, tc.want)
		}
	}
	c := newMapContext(&n8nNode{Name: "n", Parameters: map[string]interface{}{}}, "w")
	if _, err := c.condTemplate("c", condSet{conds: []cond{{"a", "^a", "string", "regex"}}}); err == nil {
		t.Error("regex should not translate to a template")
	}
}

func TestGenericLoopBackEdgeIsRemoved(t *testing.T) {
	wf, rep := convertString(t, `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"Call","type":"n8n-nodes-base.httpRequest","typeVersion":4.2,"position":[200,0],"parameters":{"url":"https://x"}},
	 {"name":"Done?","type":"n8n-nodes-base.if","typeVersion":2,"position":[400,0],"parameters":{"conditions":{"conditions":[{"leftValue":"={{ $json.done }}","operator":{"type":"boolean","operation":"true","singleValue":true}}],"combinator":"and"}}},
	 {"name":"Wait","type":"n8n-nodes-base.wait","typeVersion":1.1,"position":[600,100],"parameters":{"amount":30}}],
	 "connections":{"M":{"main":[[{"node":"Call","type":"main","index":0}]]},
	  "Call":{"main":[[{"node":"Done?","type":"main","index":0}]]},
	  "Done?":{"main":[[],[{"node":"Wait","type":"main","index":0}]]},
	  "Wait":{"main":[[{"node":"Call","type":"main","index":0}]]}}}`)
	parsed, err := workflow.ParseWorkflowFileBytes(mustJSON(t, wf))
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.ValidateForActivation(&parsed); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if rep.Counts.RemovedLoops != 1 {
		t.Errorf("loops flattened = %d", rep.Counts.RemovedLoops)
	}
	if nr := rep.Nodes[1]; nr.Status != StatusPartial {
		t.Errorf("Call should be partial after losing its back-edge: %+v", nr)
	}
	if n := nodeByName(wf, "Done?"); n.Config["mode"] != "per_item" {
		t.Errorf("if config = %v", n.Config)
	}
}

func TestErrorOutputBecomesErrorBranch(t *testing.T) {
	wf, _ := convertString(t, `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"S","type":"n8n-nodes-base.switch","typeVersion":3,"position":[200,0],"onError":"continueErrorOutput","parameters":{"rules":{"values":[{"conditions":{"conditions":[{"leftValue":"={{ $json.a }}","rightValue":"x","operator":{"type":"string","operation":"equals"}}]}}]}}},
	 {"name":"A","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[400,0],"parameters":{}},
	 {"name":"E","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[400,100],"parameters":{}}],
	 "connections":{"M":{"main":[[{"node":"S","type":"main","index":0}]]},
	  "S":{"main":[[{"node":"A","type":"main","index":0}],[{"node":"E","type":"main","index":0}]]}}}`)
	s := nodeByName(wf, "S")
	if s.Config["on_error"] != "error_branch" {
		t.Errorf("on_error = %v", s.Config["on_error"])
	}
	handles := map[string]string{}
	for _, c := range wf.Connections {
		if c.Source == s.ID {
			handles[nodeName(wf, c.Target)] = c.SourceHandle
		}
	}
	if handles["A"] != "0" || handles["E"] != "error" {
		t.Errorf("handles = %v", handles)
	}
}

func nodeName(wf *workflow.WorkflowFile, id string) string {
	for _, n := range wf.Nodes {
		if n.ID == id {
			return n.Name
		}
	}
	return ""
}

func TestParseRejectsNonWorkflows(t *testing.T) {
	for _, src := range []string{`{}`, `[]`, `[{"nodes":[]},{"nodes":[]}]`, `{"nodes":[{"name":"a"}]}`, `not json`} {
		if _, _, err := Convert([]byte(src)); err == nil {
			t.Errorf("%s: want an error", src)
		}
	}
	if !LooksLikeN8n([]byte(`{"data":{"nodes":[{"name":"a","type":"n8n-nodes-base.noOp"}],"connections":{}}}`)) {
		t.Error("wrapped export not recognised")
	}
	if LooksLikeN8n([]byte(`{"nodes":[{"id":"n1","type":"core.set","name":"x"}],"connections":[]}`)) {
		t.Error("a mono workflow was taken for n8n")
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
