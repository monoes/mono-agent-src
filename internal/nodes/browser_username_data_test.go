package nodes

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A browser or social node acts as the account its username names, with
// that account's saved session. A username an expression takes from a
// webhook's body is refused before the node runs, in an approved workflow
// too, as a credential_id from data is; one written in the workflow runs
// (#845 review: data-chosen vault entries).
func TestABrowserNodeActsOnlyAsTheAccountTheWorkflowNames(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(workflow.WithNodeState(context.Background(), nil), db.DB), "default")
	bot := &flakyBot{}
	setupSend(t, bot)
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	run := func(username string) error {
		t.Helper()
		id := "wf-" + uuid.NewString()[:8]
		wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default",
			Nodes: []workflow.WorkflowNode{
				{ID: id + "-t", WorkflowID: id, Type: "trigger.webhook", Name: "Hook"},
				{ID: id + "-s", WorkflowID: id, Type: "acme.send", Name: "Send", Config: map[string]interface{}{"username": username, "targets": []interface{}{"a"}}},
			},
			Connections: []workflow.WorkflowConnection{{ID: id + "-c", WorkflowID: id, SourceNodeID: id + "-t", SourceHandle: "main", TargetNodeID: id + "-s", TargetHandle: "main"}},
		}
		for _, err := range []error{store.CreateWorkflow(ctx, wf), store.SaveWorkflowNodes(ctx, id, wf.Nodes),
			store.SaveWorkflowConnections(ctx, id, wf.Connections), store.ApproveDefinition(ctx, id)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
		if err != nil {
			t.Fatal(err)
		}
		reg := workflow.NewNodeTypeRegistry()
		reg.Register("acme.send", func() workflow.NodeExecutor { return NewBrowserNode("acme", "send") })
		exec := &workflow.WorkflowExecution{ID: uuid.NewString(), WorkflowID: id, ProfileID: "default", TriggerType: "trigger.webhook", TriggerNodeID: id + "-t",
			TriggerData: map[string]interface{}{"body": map[string]interface{}{"u": "their-account"}}}
		if err := store.CreateExecution(ctx, exec); err != nil {
			t.Fatal(err)
		}
		return workflow.RunExecution(ctx, exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	}

	const refused = `"username": the account it acts as must be written in the workflow, not taken from its data`
	if err := run("{{ $json.body.u }}"); err == nil || !strings.Contains(err.Error(), refused) || len(bot.sent) != 0 {
		t.Errorf("an account named by the body: %v, sent %v", err, bot.sent)
	}
	if err := run("alice"); err != nil || len(bot.sent) != 1 {
		t.Errorf("the workflow's own account: %v, sent %v", err, bot.sent)
	}
}
