package workflowedit

import (
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow whose review hash cannot be worked out gets a guard that matches nothing, not an empty
// one that would let an approved edit through unchecked.
func TestReviewOfNeverReturnsAnEmptyGuardForAWorkflow(t *testing.T) {
	good := &workflow.Workflow{ID: "w", Name: "w"}
	if reviewOf(good) == "" || reviewOf(good) == unreadableReview {
		t.Fatalf("a readable workflow: %q", reviewOf(good))
	}
	bad := &workflow.Workflow{ID: "w", Nodes: []workflow.WorkflowNode{{ID: "n", Config: map[string]interface{}{"f": func() {}}}}}
	if got := reviewOf(bad); got != unreadableReview {
		t.Fatalf("an unhashable workflow got %q", got)
	}
	if reviewOf(nil) != "" {
		t.Fatal("no workflow has no guard")
	}
}
