package workflow

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

type refusalProbe struct{ why *string }

func (refusalProbe) Type() string { return "test.refusalprobe" }
func (n refusalProbe) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*n.why = secrets.ResolveRefusal(ctx)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// A run of a workflow the user never approved reads no secret, unless the
// profile's owner turned agent access on (`secret agent-access on`). The
// approved run is the same either way, and turning it off refuses again.
func TestAgentAccessLiftsTheSecretRefusalOfAnUnapprovedRun(t *testing.T) {
	store := newFullEngineStore(t)
	db := store.RawDB()
	bg := context.Background()
	for _, m := range []string{"106_workflow_approvals.sql", "110_profile_agent_secret_access.sql", "111_agent_secret_access_scope_audit.sql", "125_vault_agent_access_levels.sql"} {
		sql, err := data.MigrationsFS.ReadFile("migrations/" + m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS workflow_versions (workflow_id TEXT, version INTEGER, definition_hash TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(sql)); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}

	var why string
	reg := NewNodeTypeRegistry()
	reg.Register("test.refusalprobe", func() NodeExecutor { return refusalProbe{why: &why} })
	wf := &Workflow{ID: "wf-agent", Name: "From an agent", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-agent", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "p", WorkflowID: "wf-agent", Type: "test.refusalprobe", Name: "Probe", Config: map[string]interface{}{"n": 1}},
		},
		Connections: []WorkflowConnection{{ID: "c", WorkflowID: "wf-agent", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
	}
	if err := store.CreateWorkflow(bg, wf); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowNodes(bg, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowConnections(bg, wf.ID, wf.Connections); err != nil {
		t.Fatal(err)
	}
	run := func() string {
		t.Helper()
		cur, err := store.GetWorkflow(bg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		dag, err := BuildDAG(cur.Nodes, cur.Connections)
		if err != nil {
			t.Fatal(err)
		}
		why = "unset"
		ctx := vault.ContextWithProfileID(vault.ContextWithDB(bg, db), "default")
		exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "t"}
		if err := RunExecution(ctx, exec, cur, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
			t.Fatalf("RunExecution: %v", err)
		}
		return why
	}

	if got := run(); got == "" || got == "unset" {
		t.Fatalf("by default an agent's workflow reads no secret, refusal %q", got)
	}
	if err := secrets.SetAgentAccess(bg, db, "default", true); err != nil {
		t.Fatal(err)
	}
	if got := run(); got != "" {
		t.Errorf("with agent access on the refusal is still %q", got)
	}
	if err := secrets.SetAgentAccess(bg, db, "default", false); err != nil {
		t.Fatal(err)
	}
	if got := run(); got == "" || got == "unset" {
		t.Errorf("with agent access off again the refusal is gone: %q", got)
	}
	// Another profile's setting does not carry over.
	if err := secrets.SetAgentAccess(bg, db, "other", true); err != nil {
		t.Fatal(err)
	}
	if got := run(); got == "" || got == "unset" {
		t.Errorf("another profile's agent access lifted this one's refusal: %q", got)
	}
}
