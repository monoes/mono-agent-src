package workflow

import "testing"

// An attached chat model sub-node supplies agent.ask's runtime: without one
// a missing runtime is still reported.
func TestValidateRuntimeSuppliedByModelSubNode(t *testing.T) {
	missingRuntime := func(conns []WorkflowConnection, disabled bool) bool {
		wf := &Workflow{Nodes: []WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Start"},
			{ID: "a", Type: "agent.ask", Name: "Ask", Config: map[string]interface{}{"prompt": "hi"}},
			{ID: "m", Type: "ai.model", Name: "Model", Disabled: disabled, Config: map[string]interface{}{"runtime": "claude"}},
		}, Connections: append([]WorkflowConnection{{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "a", TargetHandle: "main"}}, conns...)}
		for _, is := range ValidateWorkflow(wf, ValidateOptions{}).Issues {
			if is.NodeID == "a" && is.Field == "runtime" && is.Code == IssueMissingRequired {
				return true
			}
		}
		return false
	}
	attach := []WorkflowConnection{{SourceNodeID: "m", SourceHandle: "ai_languageModel", TargetNodeID: "a", TargetHandle: "ai_languageModel"}}
	if missingRuntime(attach, false) {
		t.Error("runtime reported missing with a chat model attached")
	}
	if !missingRuntime(nil, false) {
		t.Error("runtime not reported missing without a chat model")
	}
	if !missingRuntime(attach, true) {
		t.Error("a disabled chat model supplied the runtime")
	}
	// A memory sub-node does not supply the runtime.
	if !missingRuntime([]WorkflowConnection{{SourceNodeID: "m", SourceHandle: "ai_memory", TargetNodeID: "a", TargetHandle: "ai_memory"}}, false) {
		t.Error("an ai_memory sub-node supplied the runtime")
	}
}
