package operator

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
	_ "modernc.org/sqlite"
)

// tokenDB is a database with the run-token table (migration 160).
func tokenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "runs.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m, err := data.MigrationsFS.ReadFile("migrations/160_workflow_run_tokens.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(m)); err != nil {
		t.Fatal(err)
	}
	return db
}

// A workflow node's child counts as an approved workflow's only when the run
// it carries the token of is recorded as approved and still going, and nothing
// else marks it: a child that drops or forges the token, or whose store cannot
// be read, is held like an unapproved run's.
func TestApprovedWorkflowChild(t *testing.T) {
	bg := context.Background()
	db := tokenDB(t)
	good, _ := workflow.IssueRunToken(bg, db, "exec-1", true)
	bad, _ := workflow.IssueRunToken(bg, db, "exec-2", false)
	ended, _ := workflow.IssueRunToken(bg, db, "exec-3", true)
	if err := workflow.EndRunToken(bg, db, ended); err != nil {
		t.Fatal(err)
	}
	closed := tokenDB(t)
	closedTok, _ := workflow.IssueRunToken(bg, closed, "exec-4", true)
	closed.Close()

	run := map[string]string{workflow.EnvWorkflowRun: "exec-1"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range run {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		name string
		env  map[string]string
		db   *sql.DB
		want bool
	}{
		{"approved run's child with its token", with(map[string]string{workflow.EnvRunToken: good}), db, true},
		{"approved run's child that dropped the token", run, db, false},
		{"child with a forged token", with(map[string]string{workflow.EnvRunToken: "0123456789abcdef0123456789abcdef"}), db, false},
		{"unapproved run's child with its token", with(map[string]string{workflow.EnvRunToken: bad}), db, false},
		{"unapproved run's child that wipes the old env mark", with(map[string]string{workflow.EnvRunToken: bad, workflow.EnvUnapprovedRun: ""}), db, false},
		{"token of a finished execution", with(map[string]string{workflow.EnvRunToken: ended}), db, false},
		{"unreadable database", with(map[string]string{workflow.EnvRunToken: closedTok}), closed, false},
		{"no database", with(map[string]string{workflow.EnvRunToken: good}), nil, false},
		{"token without a workflow mark", map[string]string{workflow.EnvRunToken: good}, db, false},
		{"chat's run under a workflow", with(map[string]string{workflow.EnvRunToken: good, browserctl.EnvAICaller: "chat"}), db, false},
		{"an agent under a workflow", with(map[string]string{workflow.EnvRunToken: good, "CLAUDECODE": "1"}), db, false},
		{"the chat", map[string]string{browserctl.EnvAICaller: "chat"}, db, false},
		{"the operator", nil, db, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range append(MarkerEnv(), workflow.EnvUnapprovedRun, workflow.EnvRunToken) {
				t.Setenv(k, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := ApprovedWorkflowChild(bg, c.db); got != c.want {
				t.Fatalf("ApprovedWorkflowChild = %v, want %v", got, c.want)
			}
		})
	}
}

// UnapprovedRunProcess: a process under a workflow run's mark that cannot show an approved live token.
func TestUnapprovedRunProcess(t *testing.T) {
	bg := context.Background()
	db := tokenDB(t)
	good, _ := workflow.IssueRunToken(bg, db, "exec-1", true)
	for name, c := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"approved child":         {map[string]string{workflow.EnvWorkflowRun: "exec-1", workflow.EnvRunToken: good}, false},
		"child that dropped it":  {map[string]string{workflow.EnvWorkflowRun: "exec-1"}, true},
		"the operator's process": {nil, false},
	} {
		for _, k := range append(MarkerEnv(), workflow.EnvUnapprovedRun, workflow.EnvRunToken) {
			t.Setenv(k, "")
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		if got := UnapprovedRunProcess(bg, opener(db)); got != c.want {
			t.Errorf("%s: UnapprovedRunProcess = %v, want %v", name, got, c.want)
		}
	}
}

// A process only an agent marker shows (no workflow mark at all) is not the user's terminal either: it cannot show
// an approved token, so a run it starts is marked as called by an unapproved workflow.
func TestUnapprovedRunProcessCountsAnAgentMarkerWithoutAToken(t *testing.T) {
	for _, k := range append(MarkerEnv(), workflow.EnvUnapprovedRun) {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDECODE", "1")
	if !UnapprovedRunProcess(context.Background(), opener(tokenDB(t))) {
		t.Error("an agent's process with no approved token is not marked")
	}
}

// The run token is scrubbed where the markers are: an agent or coder turn started inside a run must not keep it.
func TestMarkerEnvHoldsTheRunToken(t *testing.T) {
	found := false
	for _, k := range MarkerEnv() {
		found = found || k == workflow.EnvRunToken
	}
	if !found {
		t.Error("MarkerEnv does not list " + workflow.EnvRunToken)
	}
}

func opener(db *sql.DB) func() (*sql.DB, error) { return func() (*sql.DB, error) { return db, nil } }

// The database is opened only for a marked process: the user's own terminal opens nothing, and a marked process
// whose database cannot be opened is unapproved.
func TestUnapprovedRunProcessOpensTheDatabaseOnlyWhenMarked(t *testing.T) {
	for _, k := range append(MarkerEnv(), workflow.EnvUnapprovedRun) {
		t.Setenv(k, "")
	}
	opened := 0
	open := func() (*sql.DB, error) { opened++; return nil, context.Canceled }
	if UnapprovedRunProcess(context.Background(), open) || opened != 0 {
		t.Errorf("the user's terminal: opened %d time(s)", opened)
	}
	t.Setenv(workflow.EnvWorkflowRun, "exec-1")
	if !UnapprovedRunProcess(context.Background(), open) || opened != 1 {
		t.Errorf("a marked process whose database will not open: opened %d time(s)", opened)
	}
	if !UnapprovedRunProcess(context.Background(), nil) {
		t.Error("a marked process with no opener is not unapproved")
	}
}
