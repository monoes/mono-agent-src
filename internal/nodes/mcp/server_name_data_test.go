package mcpnode

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// mcp.call reads the server definition the workflow names. A name an
// expression takes from a webhook's body is refused before the vault is
// read, in an approved workflow too; a name written in the workflow is read
// as before. A header reference the body put in mid-text is refused too
// (#845 review: data-chosen vault entries, references inside text).
func TestCallReadsOnlyTheServerTheWorkflowNames(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "crm", map[string]string{"secret": "not a server definition"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	run := func(config map[string]interface{}, body map[string]interface{}) error {
		t.Helper()
		id := "wf-" + uuid.NewString()[:8]
		wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default",
			Nodes: []workflow.WorkflowNode{
				{ID: id + "-t", WorkflowID: id, Type: "trigger.webhook", Name: "Hook"},
				{ID: id + "-m", WorkflowID: id, Type: NodeType, Name: "Call", Config: config},
			},
			Connections: []workflow.WorkflowConnection{{ID: id + "-c", WorkflowID: id, SourceNodeID: id + "-t", SourceHandle: "main", TargetNodeID: id + "-m", TargetHandle: "main"}},
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
		reg.Register(NodeType, func() workflow.NodeExecutor { return &CallNode{} })
		exec := &workflow.WorkflowExecution{ID: uuid.NewString(), WorkflowID: id, ProfileID: "default", TriggerType: "trigger.webhook", TriggerNodeID: id + "-t",
			TriggerData: map[string]interface{}{"body": body}}
		return workflow.RunExecution(ctx, exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	}

	const refused = `"server": the vault entry's name must be written in the workflow, not taken from its data`
	server := func(name string) map[string]interface{} {
		return map[string]interface{}{"server": name, "operation": "list_tools"}
	}
	if err := run(server("{{ $json.body.server }}"), map[string]interface{}{"server": "crm"}); err == nil || !strings.Contains(err.Error(), refused) {
		t.Errorf("a server named by the body: %v", err)
	}
	// The workflow's own name is read: this entry holds no server definition, which is what fails.
	if err := run(server("crm"), nil); err == nil || strings.Contains(err.Error(), refused) || !strings.Contains(err.Error(), `server secret "crm" is not a JSON server definition`) {
		t.Errorf("the workflow's own server: %v", err)
	}

	// A header resolves references inside its text, so one the body put
	// there is refused; text without one goes on to the (absent) server.
	header := map[string]interface{}{"url": "http://127.0.0.1:1/mcp", "operation": "list_tools",
		"headers": map[string]interface{}{"Authorization": "Bearer {{ $json.body.t }}"}}
	const fromData = `"headers.Authorization": ` + "a @secret reference that came from the run's data or an expression is not read from the vault"
	if err := run(header, map[string]interface{}{"t": "@secret:crm"}); err == nil || !strings.Contains(err.Error(), fromData) {
		t.Errorf("a header secret from the body: %v", err)
	}
	if err := run(header, map[string]interface{}{"t": "plain-token"}); err == nil || strings.Contains(err.Error(), fromData) {
		t.Errorf("a header without a reference: %v", err)
	}
}
