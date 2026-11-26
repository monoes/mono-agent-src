package image

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

const removedType = "image.remove_background"

// A saved workflow that still uses the removed node fails with a message
// saying background removal was removed, not "unknown node type".
func TestRemoveBackgroundSaysItWasRemoved(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterAll(r)
	factory, ok := r.Get(removedType)
	if !ok {
		t.Fatalf("%s is not registered: a saved workflow would fail as an unknown node type", removedType)
	}
	items := []workflow.Item{workflow.NewItem(map[string]interface{}{"image_path": "/nonexistent.png"})}
	_, err := factory().Execute(context.Background(), workflow.NodeInput{Items: items}, map[string]interface{}{})
	assertSaysRemoved(t, "execute", err)

	// With no items it still says so instead of succeeding silently.
	_, err = factory().Execute(context.Background(), workflow.NodeInput{}, nil)
	assertSaysRemoved(t, "execute without items", err)
}

func TestRemoveBackgroundValidationSaysItWasRemoved(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterAll(r)
	wf := &workflow.Workflow{
		Name: "old cutout",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual"},
			{ID: "cut", Type: removedType, Config: map[string]interface{}{"field": "image_path", "bgcolor": "fff"}},
		},
		Connections: []workflow.WorkflowConnection{
			{ID: "c1", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "cut", TargetHandle: "main"},
		},
	}
	assertSaysRemoved(t, "save", workflow.ValidateForSave(wf))

	rep := workflow.ValidateWorkflow(wf, workflow.ValidateOptions{KnownNodeType: r.Has})
	if rep.Valid {
		t.Fatal("a workflow with the removed node validates")
	}
	for _, is := range rep.Issues {
		if is.Code == workflow.IssueUnknownNodeType {
			t.Errorf("reported as an unknown node type: %s", is.Message)
		}
	}
	all := strings.Join(rep.Errors, "\n")
	if !strings.Contains(all, "background removal was removed") || !strings.Contains(all, `"cut"`) {
		t.Errorf("validation errors = %q, want the removal message naming node %q", all, "cut")
	}
}

func assertSaysRemoved(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error", what)
	}
	msg := err.Error()
	for _, want := range []string{removedType, "background removal was removed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%s: %q does not contain %q", what, msg, want)
		}
	}
	// It is not one of the ai.* nodes: that transition's wording would mislead.
	if strings.Contains(msg, "local-agent") {
		t.Errorf("%s: %q blames the local-agent transition", what, msg)
	}
}
