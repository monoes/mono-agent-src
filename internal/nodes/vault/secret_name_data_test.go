package vault

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/secrets"
	vaultstore "github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// vault.secret_get reads the entry the workflow names. A name an expression
// takes from a webhook's body is refused before the vault is read, in an
// approved workflow too; a name written in the workflow reads the entry as
// before (#845 review: data-chosen vault entries).
func TestSecretGetReadsOnlyTheEntryTheWorkflowNames(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	db := vaultstore.DBFromContext(ctx)
	if _, err := secrets.Add(ctx, db, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	store := workflow.NewSQLiteWorkflowStore(db)
	run := func(name string) (string, error) {
		t.Helper()
		id := "wf-" + uuid.NewString()[:8]
		wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default",
			Nodes: []workflow.WorkflowNode{
				{ID: id + "-t", WorkflowID: id, Type: "trigger.webhook", Name: "Hook"},
				{ID: id + "-g", WorkflowID: id, Type: "vault.secret_get", Name: "Get", Config: map[string]interface{}{"name": name, "output_field": "got"}},
			},
			Connections: []workflow.WorkflowConnection{{ID: id + "-c", WorkflowID: id, SourceNodeID: id + "-t", SourceHandle: "main", TargetNodeID: id + "-g", TargetHandle: "main"}},
		}
		if err := store.CreateWorkflow(ctx, wf); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowNodes(ctx, id, wf.Nodes); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowConnections(ctx, id, wf.Connections); err != nil {
			t.Fatal(err)
		}
		if err := store.ApproveDefinition(ctx, id); err != nil {
			t.Fatal(err)
		}
		dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
		if err != nil {
			t.Fatal(err)
		}
		reg := workflow.NewNodeTypeRegistry()
		reg.Register("vault.secret_get", func() workflow.NodeExecutor { return &SecretGetNode{} })
		exec := &workflow.WorkflowExecution{ID: uuid.NewString(), WorkflowID: id, ProfileID: "default", TriggerType: "trigger.webhook", TriggerNodeID: id + "-t",
			TriggerData: map[string]interface{}{"body": map[string]interface{}{"entry": "vs"}}}
		if err := store.CreateExecution(ctx, exec); err != nil {
			t.Fatal(err)
		}
		runErr := workflow.RunExecution(ctx, exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
		nodes, err := store.GetExecutionNodeOutputs(ctx, exec.ID)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(nodes), runErr
	}

	out, err := run("{{ $json.body.entry }}")
	if err == nil || !strings.Contains(err.Error(), `"name": the vault entry's name must be written in the workflow, not taken from its data`) || strings.Contains(out, "vsecret-777") {
		t.Errorf("a name from the body: %v\n%s", err, out)
	}
	if out, err := run("vs"); err != nil || !strings.Contains(out, "vsecret-777") {
		t.Errorf("the workflow's own name: %v\n%s", err, out)
	}
}
