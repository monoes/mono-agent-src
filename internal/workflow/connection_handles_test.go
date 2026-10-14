package workflow_test

import (
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestNormalizeConnectionHandles_DefaultsEmptyToMain(t *testing.T) {
	conns := []workflow.WorkflowConnection{
		{SourceNodeID: "a", TargetNodeID: "b"},
		{SourceNodeID: "s", SourceHandle: "done", TargetNodeID: "d"},
	}
	workflow.NormalizeConnectionHandles(conns)
	if conns[0].SourceHandle != "main" || conns[0].TargetHandle != "main" {
		t.Errorf("empty handles = %q/%q, want main/main", conns[0].SourceHandle, conns[0].TargetHandle)
	}
	if conns[1].SourceHandle != "done" || conns[1].TargetHandle != "main" {
		t.Errorf("named handle = %q/%q, want done/main", conns[1].SourceHandle, conns[1].TargetHandle)
	}
}

// A workflow file whose connections leave the handles out (the QA loop.json
// shape) parses to "main" handles and runs the loop instead of skipping every
// node after the trigger.
func TestParseWorkflowFile_NoHandlesRunsLoop(t *testing.T) {
	raw := []byte(`{"name":"loop","nodes":[
		{"id":"t","type":"trigger.manual","name":"T"},
		{"id":"f","type":"test.emit","name":"F"},
		{"id":"s","type":"core.split_in_batches","name":"S","config":{"batch_size":3}},
		{"id":"b","type":"test.rec","name":"B"},
		{"id":"d","type":"test.done","name":"D"}],
	"connections":[
		{"source":"t","target":"f"},
		{"source":"f","target":"s"},
		{"source":"s","source_handle":"loop","target":"b"},
		{"source":"b","target":"s"},
		{"source":"s","source_handle":"done","target":"d"}]}`)
	wf, err := workflow.ParseWorkflowFileBytes(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, c := range wf.Connections {
		if c.SourceHandle == "" || c.TargetHandle == "" {
			t.Fatalf("connection %s→%s kept an empty handle", c.SourceNodeID, c.TargetNodeID)
		}
	}
	if err := workflow.ValidateForActivation(&wf); err != nil {
		t.Fatalf("validate: %v", err)
	}
	body, done := newRec(), newRec()
	if err := runWF(t, loopRegistry(10, body, done), &wf, &recStore{}, ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := body.snapshot(); !eqInts(got, []int{3, 3, 3, 1}) {
		t.Errorf("body calls = %v, want [3 3 3 1]", got)
	}
	if got := done.snapshot(); !eqInts(got, []int{10}) {
		t.Errorf("done calls = %v, want [10]", got)
	}
}

// Rows stored before the parse-time default (empty handles in SQLite) still
// route on "main" at run time.
func TestBuildDAG_EmptyHandleRoutesMain(t *testing.T) {
	nodes := []workflow.WorkflowNode{{ID: "a", Type: "trigger.manual"}, {ID: "b", Type: "core.set"}}
	dag, err := workflow.BuildDAG(nodes, []workflow.WorkflowConnection{{SourceNodeID: "a", TargetNodeID: "b"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if got := dag.SuccessorsOnHandle("a", "main"); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("SuccessorsOnHandle(main) = %v, want [b]", got)
	}
}
