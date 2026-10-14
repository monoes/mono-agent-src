package main

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestWorkflowEditAdapterWiring pins saveWorkflowDocument, the old name `workflow generate`
// saves through: the real store, the profile boundary of ownedWorkflow and errNotFound's exit 2.
// The commands themselves are pinned end to end by TestWorkflowSave* and
// TestWorkflowNodeAddSetPreserveEdgesCLI.
func TestWorkflowEditAdapterWiring(t *testing.T) {
	_, cfg := persistTestEnv(t)
	saved := saveDoc(t, cfg, editorDoc)
	other := otherProfileCfg(t, cfg)

	db, err := initDB(other)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := openWorkflowStore(db)
	ctx := context.Background()
	if _, err := saveWorkflowDocument(ctx, store, other.ProfileID, workflow.Workflow{ID: saved.ID, Name: "stolen"}); exitCodeFor(err) != 2 {
		t.Fatalf("another profile's id: exit %d (%v), want 2", exitCodeFor(err), err)
	}
	wf, err := saveWorkflowDocument(ctx, store, other.ProfileID, workflow.Workflow{Name: "theirs",
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "T"}}})
	if err != nil || wf.ProfileID != "other" || wf.Nodes[0].Schema == nil {
		t.Fatalf("save = %+v, %v", wf, err)
	}
	if ownedWorkflow(ctx, store, db.DB, "other", wf.ID) == nil {
		t.Fatal("the saved workflow is not found in its profile")
	}
}
