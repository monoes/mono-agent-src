package n8nimport

import (
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A Loop Over Items (splitInBatches v3) keeps its back-edge: mono runs the
// body once per batch and sends "done" once, as n8n does.
func TestLoopOverItemsBackEdgeIsKept(t *testing.T) {
	wf, rep := convertString(t, `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"Loop","type":"n8n-nodes-base.splitInBatches","typeVersion":3,"position":[200,0],"parameters":{"batchSize":2,"options":{"reset":true}}},
	 {"name":"Body","type":"n8n-nodes-base.set","typeVersion":3.4,"position":[400,100],"parameters":{"assignments":{"assignments":[{"id":"a","name":"iter","value":"={{ $runIndex }}","type":"number"}]},"includeOtherFields":true,"options":{}}},
	 {"name":"After","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[400,-100],"parameters":{}}],
	 "connections":{"M":{"main":[[{"node":"Loop","type":"main","index":0}]]},
	  "Loop":{"main":[[{"node":"After","type":"main","index":0}],[{"node":"Body","type":"main","index":0}]]},
	  "Body":{"main":[[{"node":"Loop","type":"main","index":0}]]}}}`)
	parsed, err := workflow.ParseWorkflowFileBytes(mustJSON(t, wf))
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.ValidateForActivation(&parsed); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if rep.Counts.RemovedLoops != 0 {
		t.Errorf("loops flattened = %d, want 0", rep.Counts.RemovedLoops)
	}
	loop, body, after := nodeByName(wf, "Loop"), nodeByName(wf, "Body"), nodeByName(wf, "After")
	if a, _ := body.Config["assignments"].([]interface{}); len(a) != 1 || a[0].(map[string]interface{})["value"] != "{{ $runIndex }}" {
		t.Errorf("body config = %v, want $runIndex kept", body.Config)
	}
	if loop.Config["reset"] != true {
		t.Errorf("loop config = %v, want reset", loop.Config)
	}
	want := map[[3]string]bool{
		{loop.ID, "loop", body.ID}:  true,
		{loop.ID, "done", after.ID}: true,
		{body.ID, "main", loop.ID}:  true,
	}
	for _, c := range wf.Connections {
		delete(want, [3]string{c.Source, c.SourceHandle, c.Target})
	}
	if len(want) > 0 {
		t.Errorf("missing connections %v in %+v", want, wf.Connections)
	}
}
