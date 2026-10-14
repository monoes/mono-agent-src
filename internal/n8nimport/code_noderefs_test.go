package n8nimport_test

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/workflow"
)

// An imported Code node reads other nodes with $('Name') and $node["Name"]
// and may await, as n8n code does; the import keeps that code working.
func TestImportedCodeReadsOtherNodesAndAwaits(t *testing.T) {
	const src = `{"name":"t","nodes":[
{"id":"1","name":"Start","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
{"id":"2","name":"All","type":"n8n-nodes-base.code","typeVersion":2,"position":[200,0],"parameters":{"jsCode":"const a = $('Code A').first().json.a;\nconst l = $('Code A').last().json.a;\nconst y = await Promise.resolve(5);\nreturn [{json:{a, l, y, n: $node[\"Code A\"].json.a}}];"}},
{"id":"3","name":"Each","type":"n8n-nodes-base.code","typeVersion":2,"position":[400,0],"parameters":{"mode":"runOnceForEachItem","jsCode":"const k = await Promise.resolve($('Code A').all().length);\nreturn {json:{v: $json.v, k}};"}}
],"connections":{"Start":{"main":[[{"node":"All","type":"main","index":0}]]},"All":{"main":[[{"node":"Each","type":"main","index":0}]]}}}`
	wf, rep, err := n8nimport.Convert([]byte(src))
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, nr := range rep.Nodes {
		for _, is := range nr.Issues {
			if is.Severity == n8nimport.SevWarning && strings.Contains(is.Message, "code") {
				t.Errorf("%s: unexpected warning: %s", nr.Name, is.Message)
			}
		}
	}
	outputs := map[string][]workflow.Item{"Code A": {workflow.NewItem(map[string]interface{}{"a": 1.0}), workflow.NewItem(map[string]interface{}{"a": 2.0})}}
	run := func(name string, items []workflow.Item) []workflow.Item {
		t.Helper()
		for _, n := range wf.Nodes {
			if n.Name != name {
				continue
			}
			code, err := workflow.NewExpressionEngine().EvaluateString(n.Config["code"].(string), workflow.ExpressionContext{})
			if err != nil {
				t.Fatalf("%s: render: %v", name, err)
			}
			out, err := (&control.CodeNode{}).Execute(context.Background(), workflow.NodeInput{Items: items, NodeOutputs: outputs}, map[string]interface{}{"code": code})
			if err != nil {
				t.Fatalf("%s: run: %v", name, err)
			}
			return out[0].Items
		}
		t.Fatalf("no node %q", name)
		return nil
	}
	got := run("All", nil)
	if len(got) != 1 {
		t.Fatalf("All = %v", got)
	}
	for k, want := range map[string]float64{"a": 1, "l": 2, "y": 5, "n": 1} {
		if toF(got[0].JSON[k]) != want {
			t.Errorf("All.%s = %v, want %v", k, got[0].JSON[k], want)
		}
	}
	each := run("Each", []workflow.Item{workflow.NewItem(map[string]interface{}{"v": "x"}), workflow.NewItem(map[string]interface{}{"v": "y"})})
	if len(each) != 2 || each[1].JSON["v"] != "y" || toF(each[1].JSON["k"]) != 2 {
		t.Errorf("Each = %v, want two items with k=2", each)
	}
}

func toF(v interface{}) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return -1
}

// Node's Buffer does not exist in mono code nodes; the report says so
// instead of calling the node fully mapped.
func TestImportedCodeUsingBufferIsReported(t *testing.T) {
	_, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[
{"id":"1","name":"Start","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
{"id":"2","name":"B64","type":"n8n-nodes-base.code","typeVersion":2,"position":[200,0],"parameters":{"jsCode":"return [{json:{b: Buffer.from('x').toString('base64')}}];"}}
],"connections":{"Start":{"main":[[{"node":"B64","type":"main","index":0}]]}}}`))
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, nr := range rep.Nodes {
		if nr.Name == "B64" && nr.Status == n8nimport.StatusFull {
			t.Errorf("B64 reported fully mapped: %+v", nr)
		}
	}
}
