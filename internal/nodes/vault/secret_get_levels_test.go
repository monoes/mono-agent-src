package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/secrets"
	vaultstore "github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

type failedVaultApprovals struct{ *workflow.SQLiteWorkflowStore }

func (failedVaultApprovals) DefinitionApproved(context.Context, string, string) (bool, error) {
	return false, errors.New("approval database unavailable")
}

func TestRevealDoesNotOverrideFailedApprovalCheck(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	db := vaultstore.DBFromContext(ctx)
	if _, err := secrets.Add(ctx, db, "default", "secret", "kept", map[string]string{"secret": "private-789"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(ctx, db, "default", secrets.AccessGrant{Level: "reveal", AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	store := failedVaultApprovals{workflow.NewSQLiteWorkflowStore(db)}
	wf := &workflow.Workflow{ID: "wf-failed-approval", Name: "Failed approval", ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "start", Type: "trigger.schedule"},
		{ID: "get", Type: "vault.secret_get", Config: map[string]interface{}{"name": "kept"}},
	}, Connections: []workflow.WorkflowConnection{{ID: "edge", SourceNodeID: "start", TargetNodeID: "get", SourceHandle: "main", TargetHandle: "main"}}}
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	for i := range wf.Nodes {
		wf.Nodes[i].WorkflowID = wf.ID
	}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections); err != nil {
		t.Fatal(err)
	}
	exec := &workflow.WorkflowExecution{ID: "run-failed-approval", WorkflowID: wf.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: "start"}
	if err := store.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("vault.secret_get", func() workflow.NodeExecutor { return &SecretGetNode{} })
	err = workflow.RunExecution(ctx, exec, wf, dag, reg, store, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	if err == nil || !strings.Contains(err.Error(), "approval database unavailable") || strings.Contains(err.Error(), "private-789") {
		t.Fatalf("failed approval under Reveal: %v", err)
	}
}

func TestRevealDoesNotOverrideExplicitSecretRefusal(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	db := vaultstore.DBFromContext(ctx)
	if err := secrets.SetAgentAccessGrant(ctx, db, "default", secrets.AccessGrant{Level: "reveal", AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	ctx = secrets.RefuseResolving(ctx, "an explicit refusal")
	_, err := (&SecretGetNode{}).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{"name": "kept"})
	if err == nil || !strings.Contains(err.Error(), "an explicit refusal") {
		t.Fatalf("explicit refusal under Reveal: %v", err)
	}
}

func TestAgentSecretGetLevelsAndRevocation(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	db := vaultstore.DBFromContext(ctx)
	const value = "private-password-836"
	if _, err := secrets.Add(ctx, db, "default", "login", "login", map[string]string{"password": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	input := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{}}}}
	config := map[string]interface{}{"name": "login"}
	get := &SecretGetNode{}
	for _, level := range []string{"strict", "use", "reveal"} {
		if err := secrets.SetAgentAccess(ctx, db, "default", false); err != nil {
			t.Fatal(err)
		}
		if level != "strict" {
			if err := secrets.SetAgentAccessGrant(ctx, db, "default", secrets.AccessGrant{Level: level, AnyHost: true}); err != nil {
				t.Fatal(err)
			}
		}
		run := workflow.WithUnapprovedStep(ctx, t.TempDir(), "test")
		out, err := get.Execute(run, input, config)
		if level == "reveal" {
			if err != nil || !strings.Contains(fmt.Sprint(out), value) {
				t.Fatalf("reveal: %v %v", out, err)
			}
			if err := secrets.SetAgentAccess(ctx, db, "default", false); err != nil {
				t.Fatal(err)
			}
			if out, err := get.Execute(run, input, config); err == nil || strings.Contains(fmt.Sprint(out), value) {
				t.Fatalf("revoked existing context: %v %v", out, err)
			}
		} else if err == nil || strings.Contains(fmt.Sprint(out), value) {
			t.Fatalf("%s: %v %v", level, out, err)
		}
	}
}

func TestStandaloneAgentUseReferencesStayMasked(t *testing.T) {
	ctx := newSecretsTestCtx(t)
	db := vaultstore.DBFromContext(ctx)
	const value = "private-password-927"
	if _, err := secrets.Add(ctx, db, "default", "secret", "token", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccess(ctx, db, "default", true); err != nil {
		t.Fatal(err)
	}
	run := workflow.WithUnapprovedStep(ctx, t.TempDir(), "test")
	resolved, err := secrets.Resolve(run, db, "default", "@secret:token")
	if err != nil || resolved != value {
		t.Fatalf("use reference: %q %v", resolved, err)
	}
	outputs := []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: map[string]interface{}{"unexpected_key": value}}}}}
	masked := workflow.MaskAgentNodeOutputs(run, outputs)
	if strings.Contains(fmt.Sprint(masked), value) || !strings.Contains(fmt.Sprint(outputs), value) {
		t.Fatalf("mask must copy: %v original %v", masked, outputs)
	}
	if err := secrets.SetAgentAccess(ctx, db, "default", false); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Resolve(run, db, "default", "@secret:token"); err == nil {
		t.Fatal("existing context read after revocation")
	}
}
