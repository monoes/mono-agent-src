package workflow

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/vault"
	_ "modernc.org/sqlite"
)

func runTokenDB(t *testing.T, migrate bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "tokens.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if migrate {
		sqlText, err := data.MigrationsFS.ReadFile("migrations/160_workflow_run_tokens.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(sqlText)); err != nil {
			t.Fatalf("migration 160: %v", err)
		}
	}
	return db
}

// A token is looked up by what it hashes to: approved only when the run was issued as approved and is still going.
func TestRunTokenApprovedOnlyForALiveApprovedRun(t *testing.T) {
	db, bg := runTokenDB(t, true), context.Background()
	good, err := IssueRunToken(bg, db, "exec-1", true)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := IssueRunToken(bg, db, "exec-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(good) < 32 || good == bad {
		t.Fatalf("tokens must be long and distinct: %q %q", good, bad)
	}
	for name, c := range map[string]struct {
		token string
		want  bool
	}{
		"approved run": {good, true}, "unapproved run": {bad, false}, "dropped": {"", false},
		"forged": {strings.Repeat("a", len(good)), false}, "a run's id is not its token": {"exec-1", false},
	} {
		if got, _ := RunTokenApproved(bg, db, c.token); got != c.want {
			t.Errorf("%s: approved = %v, want %v", name, got, c.want)
		}
	}
	// The execution ends: its token is dead, and only its own.
	if err := EndRunToken(bg, db, good); err != nil {
		t.Fatal(err)
	}
	if got, _ := RunTokenApproved(bg, db, good); got {
		t.Error("a token of a finished execution still approves")
	}
}

func TestRunTokenExpires(t *testing.T) {
	db, bg := runTokenDB(t, true), context.Background()
	tok, err := IssueRunToken(bg, db, "exec-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_run_tokens SET expires_at = '2000-01-01T00:00:00Z'`); err == nil {
		t.Fatal("a row's lifetime must not be editable")
	}
	// A crashed engine never ends its row: one whose lifetime lapsed is dead (inserted past, as a crash left it).
	if _, err := db.Exec(`INSERT INTO workflow_run_tokens (token_hash, execution_id, approved, created_at, expires_at) VALUES (?, 'old', 1, '2000-01-01T00:00:00Z', '2000-01-02T00:00:00Z')`, HashRunToken("old")); err != nil {
		t.Fatal(err)
	}
	if got, _ := RunTokenApproved(bg, db, "old"); got {
		t.Error("an expired token approves")
	}
	if got, _ := RunTokenApproved(bg, db, tok); !got {
		t.Error("a live token does not approve")
	}
}

// Unreadable store: held, with an error, never approved.
func TestRunTokenFailsClosedWhenTheStoreCannotBeRead(t *testing.T) {
	bg := context.Background()
	live := runTokenDB(t, true)
	tok, err := IssueRunToken(bg, live, "exec-1", true)
	if err != nil {
		t.Fatal(err)
	}
	for name, db := range map[string]*sql.DB{"no table": runTokenDB(t, false), "no database": nil} {
		if got, err := RunTokenApproved(bg, db, tok); got || err == nil {
			t.Errorf("%s: approved=%v err=%v, want held with an error", name, got, err)
		}
	}
	live.Close()
	if got, err := RunTokenApproved(bg, live, tok); got || err == nil {
		t.Errorf("closed database: approved=%v err=%v, want held with an error", got, err)
	}
	if _, err := IssueRunToken(bg, runTokenDB(t, false), "exec-1", true); err == nil {
		t.Error("a token was issued with no table to record it in")
	}
}

// The database keeps a hash, never the token; a row's verdict cannot be flipped or revived.
func TestRunTokenRowsHoldNoSecretAndCannotBeFlipped(t *testing.T) {
	db, bg := runTokenDB(t, true), context.Background()
	tok, err := IssueRunToken(bg, db, "exec-1", false)
	if err != nil {
		t.Fatal(err)
	}
	var hash, exec string
	if err := db.QueryRow(`SELECT token_hash, execution_id FROM workflow_run_tokens`).Scan(&hash, &exec); err != nil {
		t.Fatal(err)
	}
	if hash == tok || strings.Contains(hash+exec, tok) || hash != HashRunToken(tok) {
		t.Fatalf("the row keeps the token itself: %q %q", hash, exec)
	}
	if _, err := db.Exec(`UPDATE workflow_run_tokens SET approved = 1`); err == nil {
		t.Error("an unapproved row was flipped to approved")
	}
	if err := EndRunToken(bg, db, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_run_tokens SET ended_at = NULL`); err == nil {
		t.Error("an ended row was revived")
	}
}

func TestRunEnvForHandsOnlyTheToken(t *testing.T) {
	env := RunEnvFor(WithRunToken(context.Background(), "tok-1"), nil, "exec-1")
	if !hasEnv(env, EnvRunToken+"=tok-1") {
		t.Errorf("the run's token is not in the child's environment: %v", env)
	}
	for _, e := range RunEnvFor(context.Background(), nil, "exec-1") {
		if strings.HasPrefix(e, EnvRunToken+"=") {
			t.Errorf("a run with no token hands one: %v", e)
		}
	}
	// A node's own environment setting cannot replace the run's token (the last value wins).
	env = RunEnvFor(WithRunToken(context.Background(), "tok-1"), []string{EnvRunToken + "=forged"}, "exec-1")
	if env[len(env)-1] != EnvRunToken+"=tok-1" && !hasEnv(env[len(env)-2:], EnvRunToken+"=tok-1") {
		t.Errorf("the run's token is not last: %v", env)
	}
}

// tokenProbe records the token its node sees and whether the store calls it approved while the run goes on.
type tokenProbe struct {
	token    *string
	approved *bool
}

func (p tokenProbe) Execute(ctx context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*p.token = RunTokenFrom(ctx)
	*p.approved, _ = RunTokenApproved(ctx, vault.DBFromContext(ctx), *p.token)
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// The engine issues each execution a token whose verdict is the run's approval, and ends it with the run.
func TestRunExecutionIssuesATokenForTheRun(t *testing.T) {
	for _, approve := range []bool{true, false} {
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
		var live bool
		reg := NewNodeTypeRegistry()
		reg.Register("test.tokenprobe", func() NodeExecutor { return tokenProbe{token: &token, approved: &live} })
		wf := &Workflow{ID: "wf-tok", Name: "Token", ProfileID: "default",
			Nodes: []WorkflowNode{
				{ID: "t", WorkflowID: "wf-tok", Type: "trigger.schedule", Name: "Every hour"},
				{ID: "p", WorkflowID: "wf-tok", Type: "test.tokenprobe", Name: "Probe", Config: map[string]interface{}{"n": 1}},
			},
			Connections: []WorkflowConnection{{ID: "c", WorkflowID: "wf-tok", SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "p", TargetHandle: "main"}},
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
		if approve {
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
		exec := &WorkflowExecution{ID: uuid.NewString(), WorkflowID: wf.ID, TriggerType: "trigger.schedule", TriggerNodeID: "t"}
		if err := RunExecution(ctx, exec, cur, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
			t.Fatalf("RunExecution: %v", err)
		}
		if token == "" {
			t.Fatalf("approved=%v: the run handed its nodes no token", approve)
		}
		if live != approve {
			t.Errorf("approved definition %v: the token's verdict during the run is %v", approve, live)
		}
		if got, _ := RunTokenApproved(bg, db, token); got {
			t.Errorf("approved=%v: the token of a finished execution is still alive", approve)
		}
	}
}
