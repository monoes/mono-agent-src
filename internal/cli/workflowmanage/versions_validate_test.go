package workflowmanage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// QA-A U7: restoring a version that fails `workflow validate` (here an
// expression the templates cannot parse) is refused; --force restores it, with a warning.
func TestWorkflowVersionsRestoreWarnsWhenInvalid(t *testing.T) {
	cfg := newTestDB(t, "")
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	wf := &workflow.Workflow{ID: "wf-invalid", Name: "invalid", Version: 1, ProfileID: "default",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "T"},
			{ID: "s", Type: "core.set", Name: "S", Config: map[string]interface{}{"value": "{{ $json.n * 2 }}"}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "c", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "s", TargetHandle: "main"}},
	}
	for i := range wf.Nodes {
		if err := wf.Nodes[i].MarshalConfig(); err != nil {
			t.Fatal(err)
		}
	}
	err = testDeps(cfg).CreateWorkflow(context.Background(), sqliteStore(db), wf, false)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	if out, err := run(t, cfg, "versions", "restore", wf.ID, "1"); err == nil {
		t.Fatalf("restore without --force succeeded: %s", out)
	}
	out, err := run(t, cfg, "versions", "restore", wf.ID, "1", "--force")
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	var got struct {
		RestoredFrom int      `json:"restored_from"`
		Warnings     []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.RestoredFrom != 1 ||
		!strings.Contains(strings.Join(got.Warnings, "\n"), "fails `workflow validate`: node \"s\": invalid expression") {
		t.Fatalf("restore = %s (%v), want it restored with a validate warning", out, err)
	}
}
