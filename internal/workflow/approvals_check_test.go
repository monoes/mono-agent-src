package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// flakyApprovals is a store whose approvals check fails its first fails
// times.
type flakyApprovals struct {
	*SQLiteWorkflowStore
	fails int
}

func (f *flakyApprovals) DefinitionApproved(ctx context.Context, workflowID, hash string) (bool, error) {
	if f.fails > 0 {
		f.fails--
		return false, errors.New("database is locked")
	}
	return f.SQLiteWorkflowStore.DefinitionApproved(ctx, workflowID, hash)
}

// programProbe is a step that runs a program: it records its refusal.
type programProbe struct{ refused *error }

func (programProbe) Type() string { return "test.programprobe" }
func (n programProbe) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*n.refused = RefuseUnapprovedProgram(ctx, "test.programprobe", in.WorkflowID)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// A failed check of the user's approval is tried once more, so a transient
// store error doesn't refuse an approved workflow's steps; one that persists
// refuses them, and the refusal names it (#831 round 5, C4).
func TestAFailedApprovalCheckIsRetriedAndNamed(t *testing.T) {
	_, sqlStore := approvalsTestDB(t)
	ctx := context.Background()
	var refused error
	reg := NewNodeTypeRegistry()
	reg.Register("test.programprobe", func() NodeExecutor { return programProbe{refused: &refused} })
	wf := &Workflow{ID: "wf-mine", Name: "Mine", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "wf-mine-t", WorkflowID: "wf-mine", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "wf-mine-p", WorkflowID: "wf-mine", Type: "test.programprobe", Name: "Run"},
		},
		Connections: []WorkflowConnection{{ID: "wf-mine-c", WorkflowID: "wf-mine", SourceNodeID: "wf-mine-t", SourceHandle: "main", TargetNodeID: "wf-mine-p", TargetHandle: "main"}},
	}
	createSQLWorkflow(t, sqlStore, wf)
	if err := sqlStore.ApproveDefinition(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	run := func(fails int) error {
		t.Helper()
		cur, err := sqlStore.GetWorkflow(ctx, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		dag, err := BuildDAG(cur.Nodes, cur.Connections)
		if err != nil {
			t.Fatal(err)
		}
		refused = nil
		exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "wf-mine-t"}
		if err := RunExecution(ctx, exec, cur, dag, reg, &flakyApprovals{sqlStore, fails}, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
			t.Fatalf("RunExecution: %v", err)
		}
		return refused
	}
	if err := run(1); err != nil {
		t.Errorf("one failed check of an approved workflow refused its step: %v", err)
	}
	err := run(2)
	if !errors.Is(err, ErrUnapprovedProgram) || !strings.Contains(err.Error(), "database is locked") {
		t.Errorf("a check that keeps failing: %v", err)
	}
}

// echoProbe passes its config on as its item.
type echoProbe struct{}

func (echoProbe) Type() string { return "test.echoprobe" }
func (echoProbe) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(config)}}}, nil
}

// When whether the user approved a run could not be checked, a secret
// reference in it is refused with that reason and the store error, not with
// a change the user didn't make, as a program step is.
func TestASecretRefusedForAFailedApprovalCheckSaysSo(t *testing.T) {
	keyring.MockInit()
	db, sqlStore := approvalsTestDB(t)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	reg := NewNodeTypeRegistry()
	reg.Register("test.echoprobe", func() NodeExecutor { return echoProbe{} })
	wf := &Workflow{ID: "wf-ref", Name: "Ref", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "wf-ref-t", WorkflowID: "wf-ref", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "wf-ref-p", WorkflowID: "wf-ref", Type: "test.echoprobe", Name: "Echo", Config: map[string]interface{}{"v": "@secret:vs"}},
		},
		Connections: []WorkflowConnection{{ID: "wf-ref-c", WorkflowID: "wf-ref", SourceNodeID: "wf-ref-t", SourceHandle: "main", TargetNodeID: "wf-ref-p", TargetHandle: "main"}},
	}
	createSQLWorkflow(t, sqlStore, wf)
	if err := sqlStore.ApproveDefinition(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: "wf-ref-t"}
	err = RunExecution(ctx, exec, wf, dag, reg, &flakyApprovals{sqlStore, 2}, nil, NewExpressionEngine(), zerolog.Nop())
	if err == nil || !strings.Contains(err.Error(), "could not be checked (database is locked)") || strings.Contains(err.Error(), "since you last saved") ||
		strings.Contains(err.Error(), "vsecret-777") {
		t.Fatalf("a secret reference when the approval check failed: %v", err)
	}
}
