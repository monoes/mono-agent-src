package org

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `org grant` stores the hash of the workflow definition it approved, so a
// later edit by an agent is refused at run time (orggrant.Tool.CheckWorkflow).
func TestGrantAddRecordsTheApprovedWorkflowHash(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "sum", "--owned")
	g := f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "sum")
	id := g["grant"].(map[string]interface{})["id"].(string)

	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	wf, err := workflow.NewSQLiteWorkflowStore(db.DB).GetWorkflow(ctx, f.plainWF)
	if err != nil {
		t.Fatal(err)
	}
	want, err := orggrant.WorkflowHash(wf)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := orggrant.NewStore(db.DB).GetGrant(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tool := grant.Automation()
	if tool == nil || tool.WorkflowHash == "" || tool.WorkflowHash != want {
		t.Fatalf("stored hash %+v, want %s", tool, want)
	}
	if err := tool.CheckWorkflow(wf); err != nil {
		t.Fatalf("the approved workflow is refused: %v", err)
	}
}
