package workflowvalidate

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestDryRunSteps_PlanShape(t *testing.T) {
	t.Parallel()
	wf := &workflow.Workflow{
		Name: "plan-wf",
		Nodes: []workflow.WorkflowNode{
			{ID: "trigger", Type: "trigger.manual", Config: map[string]interface{}{}},
			{ID: "set", Type: "core.set", Config: map[string]interface{}{}},
		},
		Connections: []workflow.WorkflowConnection{
			{ID: "c1", SourceNodeID: "trigger", SourceHandle: "main", TargetNodeID: "set", TargetHandle: "main"},
		},
	}
	steps, err := DryRunSteps(wf)
	if err != nil {
		t.Fatalf("DryRunSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d (%+v)", len(steps), steps)
	}
	// Trigger nodes are seeded first in the topological queue.
	if steps[0].ID != "trigger" || steps[0].Order != 1 || steps[0].Type != "trigger.manual" {
		t.Fatalf("unexpected first step: %+v", steps[0])
	}
	if steps[1].ID != "set" || steps[1].Order != 2 || steps[1].Type != "core.set" {
		t.Fatalf("unexpected second step: %+v", steps[1])
	}
}

func TestDryRunSteps_RejectsCycle(t *testing.T) {
	t.Parallel()
	wf := &workflow.Workflow{
		Name: "cycle-wf",
		Nodes: []workflow.WorkflowNode{
			{ID: "trigger", Type: "trigger.manual", Config: map[string]interface{}{}},
			{ID: "a", Type: "core.set", Config: map[string]interface{}{}},
			{ID: "b", Type: "core.set", Config: map[string]interface{}{}},
		},
		Connections: []workflow.WorkflowConnection{
			{ID: "c1", SourceNodeID: "trigger", SourceHandle: "main", TargetNodeID: "a", TargetHandle: "main"},
			{ID: "c2", SourceNodeID: "a", SourceHandle: "main", TargetNodeID: "b", TargetHandle: "main"},
			{ID: "c3", SourceNodeID: "b", SourceHandle: "main", TargetNodeID: "a", TargetHandle: "main"},
		},
	}
	if _, err := DryRunSteps(wf); err == nil {
		t.Fatal("expected cycle to be rejected")
	}
}

func TestDryRunSteps_MissingTrigger(t *testing.T) {
	t.Parallel()
	wf := &workflow.Workflow{
		Name:  "no-trigger",
		Nodes: []workflow.WorkflowNode{{ID: "set", Type: "core.set", Config: map[string]interface{}{}}},
	}
	_, err := DryRunSteps(wf)
	if !errors.Is(err, workflow.ErrNoTriggerNode) {
		t.Fatalf("expected ErrNoTriggerNode, got %v", err)
	}
	if code := clicore.ExitCodeFor(err); code != 3 {
		t.Fatalf("expected exit code 3 for missing trigger, got %d", code)
	}
}
