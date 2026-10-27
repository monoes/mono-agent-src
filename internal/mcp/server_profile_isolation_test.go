package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestCrossProfileToolsReturnNotFound is a regression test for a profile
// isolation bypass: workflow_get, workflow_validate (id form), and
// workflow_status previously loaded records straight from the store with no
// ownership check, letting any MCP client that had learned another
// profile's workflow/execution ID read that profile's full workflow
// definition or execution output. Every other surface in this codebase
// (internal/httpapi/handlers.go, internal/workflow/engine.go's
// checkWorkflowProfile) treats a cross-profile lookup as "not found"; these
// three tools must do the same.
//
// All tool calls are issued through a single Server.Serve invocation:
// Serve closes the lazily-built runtime (including the shared *sql.DB) via
// a deferred closeRuntime when the request stream ends, so driving these
// checks through separate serveLines calls on the same *Server would tear
// the database down after the first call and make every later SQL-backed
// lookup fail with "database is closed" regardless of this fix.
func TestCrossProfileToolsReturnNotFound(t *testing.T) {
	s := newTestServer(t)
	rt, err := s.runtime()
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	ctx := context.Background()
	const otherProfile = "other-profile"

	if _, err := rt.store.RawDB().Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES (?, ?)`, otherProfile, otherProfile); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	wf := &workflow.Workflow{
		ID:        "wf-owned-by-other-profile",
		Name:      "Someone else's workflow",
		ProfileID: otherProfile,
	}
	if err := rt.store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	exec := &workflow.WorkflowExecution{
		ID:          "exec-owned-by-other-profile",
		WorkflowID:  wf.ID,
		ProfileID:   otherProfile,
		Status:      "SUCCESS",
		TriggerType: "manual",
	}
	if err := rt.store.CreateExecution(ctx, exec); err != nil {
		t.Fatalf("seed execution: %v", err)
	}

	sameWF := &workflow.Workflow{ID: "wf-owned-by-default", Name: "Mine", ProfileID: "default"}
	if err := rt.store.CreateWorkflow(ctx, sameWF); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	resps := serveLines(t, s,
		callToolReq(1, "workflow_get", map[string]interface{}{"id": wf.ID}),
		callToolReq(2, "workflow_validate", map[string]interface{}{"id": wf.ID}),
		callToolReq(3, "workflow_status", map[string]interface{}{"execution_id": exec.ID}),
		callToolReq(4, "workflow_get", map[string]interface{}{"id": sameWF.ID}),
	)
	if len(resps) != 4 {
		t.Fatalf("expected 4 responses, got %d", len(resps))
	}

	assertNotFound := func(t *testing.T, id, label string) {
		t.Helper()
		text, isErr := toolText(t, respByID(t, resps, id))
		if !isErr {
			t.Fatalf("%s on another profile's record must be a tool error, got: %s", label, text)
		}
		if !strings.Contains(text, "not found") {
			t.Errorf("%s: expected a not-found-shaped error, got: %s", label, text)
		}
		if strings.Contains(text, "different profile") || strings.Contains(text, "forbidden") {
			t.Errorf("%s: error must not distinguish cross-profile from not-found: %s", label, text)
		}
	}

	t.Run("workflow_get", func(t *testing.T) { assertNotFound(t, "1", "workflow_get") })
	t.Run("workflow_validate", func(t *testing.T) { assertNotFound(t, "2", "workflow_validate") })
	t.Run("workflow_status", func(t *testing.T) { assertNotFound(t, "3", "workflow_status") })

	// Sanity check: the same lookup succeeds for the owning profile so the
	// new check doesn't just blanket-reject everything.
	t.Run("same_profile_still_works", func(t *testing.T) {
		text, isErr := toolText(t, respByID(t, resps, "4"))
		if isErr {
			t.Fatalf("workflow_get on own profile's workflow must succeed, got error: %s", text)
		}
	})
}
