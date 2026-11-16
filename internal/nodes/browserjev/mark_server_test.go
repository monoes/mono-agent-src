package browserjev

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/workflow"
	_ "modernc.org/sqlite"
)

func clearMarkEnv(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), EnvAICaller, workflow.EnvUnapprovedRun, workflow.EnvWorkflowRun, workflow.EnvRunToken) {
		t.Setenv(k, "")
	}
}

func fakeTurn(t *testing.T, in bool, err error) {
	t.Helper()
	serverInTurn = func(string) (bool, error) { return in, err }
	t.Cleanup(func() { serverInTurn = coderhost.CallerInARegisteredTurn })
}

// A leftover MONOAGENT_AI_CALLER=chat in a process a coder turn runs is marked as the stricter "other", as
// `workflow run` does; a turn check that fails counts as a turn; outside a turn chat stays chat.
func TestServerMarkChatInATurnIsOther(t *testing.T) {
	clearMarkEnv(t)
	t.Setenv(EnvAICaller, browserctl.KindChat)
	for _, c := range []struct {
		name string
		in   bool
		err  error
		want string
	}{
		{"in a turn", true, nil, browserctl.KindOther},
		{"check failed", false, errors.New("unreadable"), browserctl.KindOther},
		{"not in a turn", false, nil, browserctl.KindChat},
	} {
		fakeTurn(t, c.in, c.err)
		td := map[string]interface{}{}
		MarkAICallerForServer(context.Background(), nil, "x.db", td, "an MCP caller")
		if got := td[workflow.AICallerKey]; got != c.want {
			t.Errorf("%s: mark %v, want %s", c.name, got, c.want)
		}
	}
}

// A server under a workflow node's process marks its runs unapproved-workflow only when that process cannot
// show the token of an approved, live run: the profile's database must reach the check.
func TestServerMarkChecksTheRunToken(t *testing.T) {
	clearMarkEnv(t)
	fakeTurn(t, false, nil)
	bg := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "runs.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m, _ := data.MigrationsFS.ReadFile("migrations/160_workflow_run_tokens.sql")
	if _, err := db.Exec(string(m)); err != nil {
		t.Fatal(err)
	}
	good, _ := workflow.IssueRunToken(bg, db, "exec-1", true)
	bad, _ := workflow.IssueRunToken(bg, db, "exec-2", false)
	for _, c := range []struct {
		name, tok string
		marks     bool
	}{{"approved run's process", good, false}, {"unapproved run's process", bad, true}, {"no token", "", true}} {
		t.Setenv(workflow.EnvWorkflowRun, "exec-x")
		t.Setenv(workflow.EnvRunToken, c.tok)
		td := map[string]interface{}{}
		MarkAICallerForServer(bg, db, "x.db", td, "an API caller")
		if got := td[workflow.AICallerKey] == workflow.UnapprovedWorkflowCaller; got != c.marks {
			t.Errorf("%s: mark %v, want unapproved=%v", c.name, td[workflow.AICallerKey], c.marks)
		}
	}
}
