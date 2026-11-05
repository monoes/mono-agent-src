package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/vault"
)

// A child's environment starts from the engine process's own. A token that process already carries (a nested
// `workflow run` from an approved child) is never handed on: the run's own token replaces it, and a run with
// none hands none.
func TestRunEnvForNeverPassesOnAnInheritedToken(t *testing.T) {
	inherited := []string{"PATH=/bin", EnvRunToken + "=parent-approved", EnvRunToken + "=another"}
	count := func(env []string) (n int, last string) {
		for _, e := range env {
			if strings.HasPrefix(e, EnvRunToken+"=") {
				n++
				last = e
			}
		}
		return
	}
	if n, got := count(RunEnvFor(context.Background(), inherited, "exec-1")); n != 0 {
		t.Errorf("a run with no token hands the inherited one: %d entries, %q", n, got)
	}
	n, got := count(RunEnvFor(WithRunToken(context.Background(), "mine"), inherited, "exec-1"))
	if n != 1 || got != EnvRunToken+"=mine" {
		t.Errorf("the run's own token must be the only one: %d entries, last %q", n, got)
	}
}

// tokenEchoProbe puts the token it sees into its output, as a node that echoes its environment does.
type tokenEchoProbe struct{ token *string }

func (tokenEchoProbe) Type() string { return "test.tokenecho" }
func (p tokenEchoProbe) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*p.token = RunTokenFrom(ctx)
	return []NodeOutput{{Handle: "main", Items: []Item{{JSON: map[string]interface{}{"env": "X=1 " + *p.token, "list": []interface{}{*p.token}}}}}}, nil
}

// runForToken runs a one-node workflow (definition approved or not) with trigger data, and returns the store, the
// execution id, the db and the token its node saw.
func runForToken(t *testing.T, approveDef bool, triggerData map[string]interface{}) (*SQLiteWorkflowStore, string, string) {
	t.Helper()
	store := newFullEngineStore(t)
	db, bg := store.RawDB(), context.Background()
	for _, m := range []string{"106_workflow_approvals.sql", "160_workflow_run_tokens.sql"} {
		sqlText, err := data.MigrationsFS.ReadFile("migrations/" + m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS workflow_versions (workflow_id TEXT, version INTEGER, definition_hash TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(sqlText)); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	var token string
	reg := NewNodeTypeRegistry()
	reg.Register("test.tokenecho", func() NodeExecutor { return tokenEchoProbe{token: &token} })
	wf := &Workflow{ID: "wf-h", Name: "Hardening", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-h", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "p", WorkflowID: "wf-h", Type: "test.tokenecho", Name: "Echo", Config: map[string]interface{}{"n": 1}},
		},
		Connections: []WorkflowConnection{{ID: "c", WorkflowID: "wf-h", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
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
	if approveDef {
		if err := store.ApproveDefinition(bg, wf.ID); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := store.GetWorkflow(bg, wf.ID)
	dag, err := BuildDAG(cur.Nodes, cur.Connections)
	if err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(bg, db), "default")
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "t", TriggerData: triggerData}
	if err := RunExecution(ctx, exec, cur, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}
	return store, exec.ID, token
}

// The token is a bearer credential: what the run stores of its steps never shows it, wherever a node put it.
func TestRunTokenIsMaskedInStoredNodeOutput(t *testing.T) {
	store, execID, token := runForToken(t, true, nil)
	if token == "" {
		t.Fatal("no token reached the node")
	}
	nodes, err := store.GetExecutionNodeOutputs(context.Background(), execID)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("nodes %v: %v", nodes, err)
	}
	raw, _ := json.Marshal(nodes)
	if strings.Contains(string(raw), token) {
		t.Errorf("the stored node output holds the run's token: %s", raw)
	}
	if !strings.Contains(string(raw), "***") {
		t.Errorf("the token was not replaced by a mask: %s", raw)
	}
}

// Each thing that makes a run an AI's, besides the definition, records the run as unapproved.
func TestRunTokenIsUnapprovedWhenTheTriggerSaysAnAIIsBehindTheRun(t *testing.T) {
	for name, td := range map[string]map[string]interface{}{
		"called by an unapproved workflow (caller id)": {UnapprovedCallerKey: "w1"},
		"called by an unapproved workflow (mark)":      {AICallerKey: UnapprovedWorkflowCaller},
		"started by the chat":                          {AICallerKey: "chat"},
		"started by another agent":                     {AICallerKey: "other"},
	} {
		store, execID, token := runForToken(t, true, td)
		_ = execID
		var approved, rows int
		if err := store.RawDB().QueryRow(`SELECT COALESCE(MAX(approved),0), COUNT(*) FROM workflow_run_tokens WHERE token_hash = ?`, HashRunToken(token)).Scan(&approved, &rows); err != nil || rows != 1 {
			t.Fatalf("%s: row %d: %v", name, rows, err)
		}
		if approved != 0 {
			t.Errorf("%s: the run was recorded as approved", name)
		}
	}
	if store, _, token := runForToken(t, true, nil); token == "" {
		t.Fatal("control: no token")
	} else if ok := store.RawDB().QueryRow(`SELECT 1 FROM workflow_run_tokens WHERE token_hash = ? AND approved = 1`, HashRunToken(token)).Scan(new(int)); ok != nil {
		t.Errorf("control: an approved run with no AI behind it is recorded unapproved: %v", ok)
	}
}

// A run that was left open (a crashed engine) is ended once its execution is terminal, at the next issue.
func TestIssueRunTokenEndsTheTokensOfTerminalExecutions(t *testing.T) {
	store := newFullEngineStore(t)
	db, bg := store.RawDB(), context.Background()
	m, _ := data.MigrationsFS.ReadFile("migrations/160_workflow_run_tokens.sql")
	if _, err := db.Exec(string(m)); err != nil {
		t.Fatal(err)
	}
	for id, status := range map[string]string{"dead": "FAILED", "rejected": "REJECTED", "alive": "RUNNING"} {
		if _, err := db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status) VALUES (?, 'w', ?)`, id, status); err != nil {
			t.Fatal(err)
		}
	}
	dead, _ := IssueRunToken(bg, db, "dead", true)
	rejected, _ := IssueRunToken(bg, db, "rejected", true)
	alive, _ := IssueRunToken(bg, db, "alive", true)
	if _, err := IssueRunToken(bg, db, "next", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := RunTokenApproved(bg, db, dead); ok {
		t.Error("the token of a finished execution survived the sweep")
	}
	if ok, _ := RunTokenApproved(bg, db, rejected); ok {
		t.Error("the token of a rejected execution survived the sweep")
	}
	if ok, _ := RunTokenApproved(bg, db, alive); !ok {
		t.Error("the sweep ended the token of a running execution")
	}
}

// IssueRunToken sets the lifetime itself (48 hours) from the database's clock.
func TestIssueRunTokenSetsA48HourLifetime(t *testing.T) {
	db := runTokenDB(t, true)
	if _, err := IssueRunToken(context.Background(), db, "exec-1", true); err != nil {
		t.Fatal(err)
	}
	var hours float64
	if err := db.QueryRow(`SELECT (julianday(expires_at) - julianday(created_at)) * 24 FROM workflow_run_tokens`).Scan(&hours); err != nil {
		t.Fatal(err)
	}
	if hours < 47.9 || hours > 48.1 {
		t.Errorf("lifetime %.2f hours, want 48", hours)
	}
}

// A run that cannot record itself (no table to write the token in) hands its children no token: they are held.
func TestRunWithoutAWritableTokenTableHandsNoToken(t *testing.T) {
	store := newFullEngineStore(t) // no migration 160
	db, bg := store.RawDB(), context.Background()
	var token string
	reg := NewNodeTypeRegistry()
	reg.Register("test.tokenecho", func() NodeExecutor { return tokenEchoProbe{token: &token} })
	wf := &Workflow{ID: "wf-n", Name: "NoTable", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-n", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "p", WorkflowID: "wf-n", Type: "test.tokenecho", Name: "Echo", Config: map[string]interface{}{"n": 1}},
		},
		Connections: []WorkflowConnection{{ID: "c", WorkflowID: "wf-n", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
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
	cur, _ := store.GetWorkflow(bg, wf.ID)
	dag, _ := BuildDAG(cur.Nodes, cur.Connections)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(bg, db), "default")
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "t"}
	token = "unset"
	if err := RunExecution(ctx, exec, cur, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}
	if token != "" {
		t.Errorf("a run that could not record itself handed its nodes a token: %q", token)
	}
}

// failingEcho fails with an error that carries the token it sees, as a node that reports its environment does.
type failingEcho struct{}

func (failingEcho) Type() string { return "test.failingecho" }
func (failingEcho) Execute(ctx context.Context, _ NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	return nil, errors.New("boom with " + RunTokenFrom(ctx))
}

// The run's log never shows the token either: a node error is masked before it is logged.
func TestNodeErrorIsMaskedOfTheRunTokenBeforeItIsLogged(t *testing.T) {
	store := newFullEngineStore(t)
	db, bg := store.RawDB(), context.Background()
	m, _ := data.MigrationsFS.ReadFile("migrations/160_workflow_run_tokens.sql")
	if _, err := db.Exec(string(m)); err != nil {
		t.Fatal(err)
	}
	reg := NewNodeTypeRegistry()
	reg.Register("test.failingecho", func() NodeExecutor { return failingEcho{} })
	wf := &Workflow{ID: "wf-l", Name: "Log", ProfileID: "default",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-l", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "p", WorkflowID: "wf-l", Type: "test.failingecho", Name: "Fail", Config: map[string]interface{}{"n": 1}},
		},
		Connections: []WorkflowConnection{{ID: "c", WorkflowID: "wf-l", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
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
	cur, _ := store.GetWorkflow(bg, wf.ID)
	dag, _ := BuildDAG(cur.Nodes, cur.Connections)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(bg, db), "default")
	exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "t"}
	var logs bytes.Buffer
	_ = RunExecution(ctx, exec, cur, dag, reg, store, nil, NewExpressionEngine(), zerolog.New(&logs))
	if !strings.Contains(logs.String(), "boom with") {
		t.Fatalf("the node error was not logged: %s", logs.String())
	}
	// The token is 64 hex characters: none may follow the error's text.
	_, after, _ := strings.Cut(logs.String(), "boom with ")
	if len(after) >= 64 && isHex(after[:64]) {
		t.Errorf("the log holds the run's token: %s", logs.String())
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
