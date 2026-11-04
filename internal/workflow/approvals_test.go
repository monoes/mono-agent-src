package workflow

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/data"
)

type aiAuthoredProbe struct{ seen *bool }

func (aiAuthoredProbe) Type() string { return "test.aiauthoredprobe" }
func (n aiAuthoredProbe) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*n.seen = AIAuthoredFrom(ctx)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// A workflow written straight to the store, as the chat's and MCP's tools
// do, runs as an AI's whatever triggers it (here a schedule). Once the
// operator saves or activates it, its definition runs as theirs, until a
// change by anyone else (#831 review C). Definitions a version recorded
// before the approvals existed count as approved.
func TestRunsOfDefinitionsTheOperatorNeverApprovedAreAIRuns(t *testing.T) {
	store := newFullEngineStore(t)
	db := store.RawDB()
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE workflow_versions (workflow_id TEXT, version INTEGER, definition_hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_versions VALUES ('wf-old', 1, 'hash-old'), ('wf-gone', 1, 'hash-gone')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflows (id, name) VALUES ('wf-old', 'Old')`); err != nil {
		t.Fatal(err)
	}
	migration, err := data.MigrationsFS.ReadFile("migrations/106_workflow_approvals.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.DefinitionApproved(ctx, "wf-old", "hash-old"); err != nil || !ok {
		t.Fatalf("a definition versioned before the migration: %v %v", ok, err)
	}
	if ok, _ := store.DefinitionApproved(ctx, "wf-gone", "hash-gone"); ok {
		t.Fatal("a version of a workflow that is gone was approved")
	}

	var seen bool
	reg := NewNodeTypeRegistry()
	reg.Register("test.aiauthoredprobe", func() NodeExecutor { return aiAuthoredProbe{seen: &seen} })
	wf := &Workflow{ID: "wf-chat", Name: "From the chat", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-chat", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "p", WorkflowID: "wf-chat", Type: "test.aiauthoredprobe", Name: "Probe", Config: map[string]interface{}{"n": 1}},
		},
		Connections: []WorkflowConnection{{ID: "c", WorkflowID: "wf-chat", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
	}
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections); err != nil {
		t.Fatal(err)
	}
	run := func() bool {
		t.Helper()
		cur, err := store.GetWorkflow(ctx, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		dag, err := BuildDAG(cur.Nodes, cur.Connections)
		if err != nil {
			t.Fatal(err)
		}
		seen = false
		exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "t"}
		if err := RunExecution(ctx, exec, cur, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
			t.Fatalf("RunExecution: %v", err)
		}
		return seen
	}

	if !run() {
		t.Error("a scheduled run of a workflow the chat made is the operator's")
	}
	if err := store.ApproveDefinition(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	if run() {
		t.Error("after the operator's save, a scheduled run is still an AI's")
	}
	wf.Nodes[1].Config = map[string]interface{}{"n": 2}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if !run() {
		t.Error("after a change the operator did not make, a run is the operator's")
	}
}

// A store that keeps no approvals approves every run.
func TestAStoreWithoutApprovalsApprovesEveryRun(t *testing.T) {
	if ok, _ := operatorApproved(context.Background(), &stubStore{}, &Workflow{ID: "w"}); !ok {
		t.Error("a store without approvals marked a run")
	}
}

// The verdict of the command that runs a node travels in ctx: unset, it is "" (every run the engine
// starts), and an empty verdict leaves ctx alone.
func TestAgentCallerTravelsInTheContext(t *testing.T) {
	ctx := context.Background()
	if got := AgentCallerFrom(ctx); got != "" {
		t.Fatalf("a bare context carries the verdict %q", got)
	}
	if got := AgentCallerFrom(WithAgentCaller(ctx, "")); got != "" {
		t.Fatalf("an empty verdict was stored: %q", got)
	}
	if got := AgentCallerFrom(WithAgentCaller(ctx, "in a coder turn")); got != "in a coder turn" {
		t.Fatalf("verdict = %q", got)
	}
}
