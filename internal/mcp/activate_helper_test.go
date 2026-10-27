package mcp

import (
	"context"
	"testing"
)

// approveForTest records the saved workflow's definition as the operator's, as `workflow activate`
// or a save from the app does: a workflow an agent saves through a tool is neither active nor
// approved, and a tool cannot switch it on until the operator has approved it.
func approveForTest(t *testing.T, s *Server, id string) {
	t.Helper()
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.store.ApproveDefinition(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

// activateForTest switches a saved workflow on the way the operator does: approve, then activate.
func activateForTest(t *testing.T, s *Server, id string) {
	t.Helper()
	approveForTest(t, s, id)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.store.SetWorkflowActive(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
}
