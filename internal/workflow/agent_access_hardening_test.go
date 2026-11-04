package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// Review of #943: the profile-wide switch is audited, can be narrowed to
// secrets, hosts and a time, never shows a value it let an unapproved run
// read, and leaves the run unapproved otherwise.

const (
	valVS    = "vsecret-777"
	valOther = "other-secret-9"
)

// renameProbe copies the previous step's "v" to a field of another name, as
// a Set node renames one.
type renameProbe struct{}

func (renameProbe) Type() string { return "test.renameprobe" }
func (renameProbe) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	v := ""
	if len(in.Items) > 0 {
		v, _ = in.Items[0].JSON["v"].(string)
	}
	return []NodeOutput{{Handle: "main", Items: []Item{NewItem(map[string]interface{}{"harmless_label": v})}}}, nil
}

// failProbe fails with the value in its message, as a provider's error echoes a key.
type failProbe struct{}

func (failProbe) Type() string { return "test.failprobe" }
func (failProbe) Execute(_ context.Context, _ NodeInput, config map[string]interface{}) ([]NodeOutput, error) {
	return nil, fmt.Errorf("provider rejected key %v", config["v"])
}

type accessEnv struct {
	db    *storage.Database
	store *SQLiteWorkflowStore
	ctx   context.Context
	reg   *NodeTypeRegistry
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()
	keyring.MockInit()
	db, store := approvalsTestDB(t)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	for name, v := range map[string]string{"vs": valVS, "other": valOther} {
		if _, err := secrets.Add(ctx, db.DB, "default", "secret", name, map[string]string{"secret": v}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	e := &accessEnv{db: db, store: store, ctx: ctx, reg: NewNodeTypeRegistry()}
	e.reg.Register("test.echoprobe", func() NodeExecutor { return echoProbe{} })
	e.reg.Register("test.renameprobe", func() NodeExecutor { return renameProbe{} })
	e.reg.Register("test.failprobe", func() NodeExecutor { return failProbe{} })
	e.reg.Register("test.revokeprobe", func() NodeExecutor {
		return nodeFunc{typ: "test.revokeprobe", fn: func(ctx context.Context, in NodeInput) error {
			return secrets.SetAgentAccess(ctx, db.DB, "default", false)
		}}
	})
	return e
}

// nodeFunc is a pass-through step that runs fn.
type nodeFunc struct {
	typ string
	fn  func(context.Context, NodeInput) error
}

func (n nodeFunc) Type() string { return n.typ }
func (n nodeFunc) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	if err := n.fn(ctx, in); err != nil {
		return nil, err
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// chain makes a workflow of the trigger and the steps in a line.
func chain(id string, steps ...WorkflowNode) *Workflow {
	wf := &Workflow{ID: id, Name: id, ProfileID: "default"}
	wf.Nodes = append(wf.Nodes, WorkflowNode{ID: id + "-t", WorkflowID: id, Type: "trigger.schedule", Name: "Every hour"})
	prev := id + "-t"
	for _, s := range steps {
		s.WorkflowID = id
		wf.Nodes = append(wf.Nodes, s)
		wf.Connections = append(wf.Connections, WorkflowConnection{ID: prev + ">" + s.ID, WorkflowID: id, SourceNodeID: prev, SourceHandle: "main", TargetNodeID: s.ID, TargetHandle: "main"})
		prev = s.ID
	}
	return wf
}

func step(id, typ, name string, cfg map[string]interface{}) WorkflowNode {
	return WorkflowNode{ID: id, Type: typ, Name: name, Config: cfg}
}

// run runs the never-approved workflow wf once through store and returns the
// execution id and what RunExecution returned.
func (e *accessEnv) run(t *testing.T, wf *Workflow, store WorkflowStore) (string, error) {
	t.Helper()
	createSQLWorkflow(t, e.store, wf)
	cur, err := e.store.GetWorkflow(e.ctx, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	dag, err := BuildDAG(cur.Nodes, cur.Connections)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		store = e.store
	}
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: wf.ID + "-t"}
	if err := e.store.CreateExecution(e.ctx, exec); err != nil {
		t.Fatal(err)
	}
	err = RunExecution(e.ctx, exec, cur, dag, e.reg, store, nil, NewExpressionEngine(), zerolog.Nop())
	return exec.ID, err
}

// runStored runs the stored workflow cur once.
func (e *accessEnv) runStored(t *testing.T, cur *Workflow) (string, error) {
	t.Helper()
	dag, err := BuildDAG(cur.Nodes, cur.Connections)
	if err != nil {
		t.Fatal(err)
	}
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: cur.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: cur.ID + "-t"}
	if err := e.store.CreateExecution(e.ctx, exec); err != nil {
		t.Fatal(err)
	}
	err = RunExecution(e.ctx, exec, cur, dag, e.reg, e.store, nil, NewExpressionEngine(), zerolog.Nop())
	return exec.ID, err
}

func (e *accessEnv) stored(t *testing.T, execID string) string {
	t.Helper()
	ex, err := e.store.GetExecution(e.ctx, execID)
	if err != nil || ex == nil {
		t.Fatalf("execution %s: %v", execID, err)
	}
	b, _ := json.Marshal(ex.Nodes)
	return string(b) + ex.ErrorMessage
}

func (e *accessEnv) grant(t *testing.T, g secrets.AccessGrant) {
	t.Helper()
	if err := secrets.SetAgentAccessGrant(e.ctx, e.db.DB, "default", g); err != nil {
		t.Fatal(err)
	}
}

func TestAgentAccessAuditsEverySecretAnUnapprovedRunReads(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	wf := chain("wf-audit", step("a1", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs"}))
	execID, err := e.run(t, wf, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rows, err := secrets.AgentAccessAudit(e.ctx, e.db.DB, "default")
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows: %v %v", rows, err)
	}
	r := rows[0]
	if r.Secret != "vs" || r.WorkflowID != "wf-audit" || r.ExecutionID != execID || r.NodeID != "a1" || r.NodeName != "Echo" || r.At == "" {
		t.Errorf("audit row: %+v", r)
	}
	if b, _ := json.Marshal(rows); strings.Contains(string(b), valVS) {
		t.Errorf("the audit holds a value: %s", b)
	}
	// A run that reads nothing leaves no row; an approved run is not audited.
	approved := chain("wf-approved", step("p1", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs"}))
	createSQLWorkflow(t, e.store, approved)
	if err := e.store.ApproveDefinition(e.ctx, approved.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.store.GetWorkflow(e.ctx, approved.ID)
	dag, _ := BuildDAG(cur.Nodes, cur.Connections)
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: approved.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: "wf-approved-t"}
	if err := RunExecution(e.ctx, exec, cur, dag, e.reg, e.store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatal(err)
	}
	if rows, _ := secrets.AgentAccessAudit(e.ctx, e.db.DB, "default"); len(rows) != 1 {
		t.Errorf("an approved run was audited: %v", rows)
	}
	// Clearing is the store's; it says how many.
	if n, err := secrets.ClearAgentAccessAudit(e.ctx, e.db.DB, "default"); err != nil || n != 1 {
		t.Errorf("clear: %d %v", n, err)
	}
}

func TestAgentAccessMasksResolvedValuesInStoredOutputs(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	// A field the key-name redaction does not know, a plain "value" field, a
	// rename by a later step, and a value in the middle of a longer text.
	wf := chain("wf-mask",
		step("m1", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs", "value": "@secret:other"}),
		step("m2", "test.renameprobe", "Rename", nil),
	)
	execID, err := e.run(t, wf, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := e.stored(t, execID)
	for _, v := range []string{valVS, valOther} {
		if strings.Contains(got, v) {
			t.Errorf("a secret value is in the stored run: %s", got)
		}
	}
	if !strings.Contains(got, "harmless_label") || !strings.Contains(got, "***") {
		t.Errorf("the stored run lost its shape: %s", got)
	}
}

func TestAgentAccessMasksAValueInAnErrorAndInTheReturnedError(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	wf := chain("wf-fail", step("f1", "test.failprobe", "Fail", map[string]interface{}{"v": "@secret:vs"}))
	execID, err := e.run(t, wf, nil)
	if err == nil {
		t.Fatal("the failing step did not fail the run")
	}
	if strings.Contains(err.Error(), valVS) || strings.Contains(e.stored(t, execID), valVS) {
		t.Errorf("a value is in the error: %v / %s", err, e.stored(t, execID))
	}
}

func TestAgentAccessScopeOnlyNamedSecrets(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{Secrets: []string{"vs"}, AnyHost: true})
	if _, err := e.run(t, chain("wf-in", step("s1", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs"})), nil); err != nil {
		t.Fatalf("the named secret: %v", err)
	}
	execID, err := e.run(t, chain("wf-out", step("s2", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:other"})), nil)
	if !errors.Is(err, secrets.ErrResolveRefused) {
		t.Fatalf("a secret the grant does not name: %v", err)
	}
	if strings.Contains(err.Error(), valOther) || strings.Contains(e.stored(t, execID), valOther) {
		t.Error("a refused value was shown")
	}
	if rows, _ := secrets.AgentAccessAudit(e.ctx, e.db.DB, "default"); len(rows) != 1 || rows[0].Secret != "vs" {
		t.Errorf("only the read secret is audited: %v", rows)
	}
}

func TestAgentAccessExpiry(t *testing.T) {
	e := newAccessEnv(t)
	soon := time.Now().Add(time.Hour)
	e.grant(t, secrets.AccessGrant{AnyHost: true, ExpiresAt: &soon})
	if _, err := e.run(t, chain("wf-live", step("x1", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs"})), nil); err != nil {
		t.Fatalf("before it expires: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	e.grant(t, secrets.AccessGrant{AnyHost: true, ExpiresAt: &past})
	if on, _ := secrets.AgentAccess(e.ctx, e.db.DB, "default"); on {
		t.Error("an expired grant is on")
	}
	if _, err := e.run(t, chain("wf-dead", step("x2", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs"})), nil); !errors.Is(err, secrets.ErrResolveRefused) {
		t.Errorf("after it expired: %v", err)
	}
}

func TestAgentAccessRevokedMidFlightRefusesTheNextRead(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	wf := chain("wf-revoke",
		step("r1", "test.echoprobe", "First", map[string]interface{}{"v": "@secret:vs"}),
		step("r2", "test.revokeprobe", "Owner turns it off", nil),
		step("r3", "test.echoprobe", "Second", map[string]interface{}{"v": "@secret:other"}),
	)
	execID, err := e.run(t, wf, nil)
	if !errors.Is(err, secrets.ErrResolveRefused) || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("the read after the owner turned it off: %v", err)
	}
	if strings.Contains(e.stored(t, execID), valOther) {
		t.Error("the second secret was read")
	}
}

func TestAgentAccessIsNotGrantedWhenTheApprovalCheckFails(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	_, err := e.run(t, chain("wf-flaky", step("k1", "test.echoprobe", "Echo", map[string]interface{}{"v": "@secret:vs"})), &flakyApprovals{e.store, 2})
	if err == nil || !strings.Contains(err.Error(), "could not be checked") || strings.Contains(err.Error(), valVS) {
		t.Fatalf("an approval check that failed lifted the refusal: %v", err)
	}
	if rows, _ := secrets.AgentAccessAudit(e.ctx, e.db.DB, "default"); len(rows) != 0 {
		t.Errorf("a refused read was audited as a read: %v", rows)
	}
}

// The run is still an unapproved one under agent access: the saved
// connection's host binding keeps refusing a host the connection does not name.
func TestAgentAccessLeavesTheRunUnapproved(t *testing.T) {
	e := newAccessEnv(t)
	e.grant(t, secrets.AccessGrant{AnyHost: true})
	var authored bool
	var destErr error
	e.reg.Register("test.aiprobe", func() NodeExecutor {
		return nodeFunc{typ: "test.aiprobe", fn: func(ctx context.Context, _ NodeInput) error {
			authored = AIAuthoredFrom(ctx)
			cfg := map[string]interface{}{"url": "https://evil.example/hook"}
			destErr = CheckDestinationKeys([][]string{{"url"}}, &CredentialUse{Name: "crm"}, !AIAuthoredFrom(ctx), cfg, cfg)
			return nil
		}}
	})
	if _, err := e.run(t, chain("wf-host", step("h1", "test.aiprobe", "Probe", nil)), nil); err != nil {
		t.Fatal(err)
	}
	if !authored || !errors.Is(destErr, ErrCredentialDestination) {
		t.Errorf("AIAuthoredFrom=%v, a connection's secret to a foreign host: %v", authored, destErr)
	}
}
