package people

import (
	"context"
	"database/sql"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// hilsend.Guard tells an agent by its own markers or by an unapproved run in
// the context. A mail send the commands start from the chat's run, or from a
// process a workflow node started in a run the user never approved (or one
// such a run called), reaches the guard as an unapproved run and waits for
// approval; a process an approved workflow started and the user's terminal
// reach it as the user's.
func TestPeopleMessageSendsFromAMarkedProcessReachTheGuardUnapproved(t *testing.T) {
	for _, c := range []struct {
		name, env, val string
		want           bool
	}{
		{"unapproved workflow's child", workflow.EnvUnapprovedRun, "1", true},
		{"unapproved run's child with its token", workflow.EnvRunToken, "unapproved", true},
		{"approved workflow's child that dropped the token", workflow.EnvWorkflowRun, "exec-1", true},
		{"approved workflow's child", workflow.EnvRunToken, "approved", false},
		{"chat's run", browserctl.EnvAICaller, browserctl.KindChat, true},
		{"operator terminal", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			asOperator(t)
			t.Setenv(workflow.EnvUnapprovedRun, "")
			t.Setenv(workflow.EnvRunToken, "")
			if c.env != "" && c.env != workflow.EnvRunToken {
				t.Setenv(c.env, c.val)
			}
			if c.env == workflow.EnvUnapprovedRun || c.env == workflow.EnvRunToken {
				t.Setenv(workflow.EnvWorkflowRun, "exec-1") // every node's child has it too
			}
			dbPath := newMessagesCLITestDB(t)
			switch c.val { // the engine hands a child the token of its run
			case "approved":
				t.Setenv(workflow.EnvRunToken, runToken(t, dbPath, true))
			case "unapproved":
				t.Setenv(workflow.EnvRunToken, runToken(t, dbPath, false))
			}
			seedPersonMessage(t, dbPath, "m-in", "EXT-IN", "inbound", "alice@x.com", "read", `{"connection_id":"outlook"}`)
			stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
				return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "EXT-NEW"}), nil
			})
			for _, args := range [][]string{
				{"messages", "compose", "p1", "--connection", "outlook", "--subject", "Hi", "--body", "Hello Alice"},
				{"messages", "reply", "m-in", "--connection", "outlook", "--body", "Thanks"},
			} {
				var seen []bool
				workflow.SetExecGuard(func(ctx context.Context, _ string, _ workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, bool, error) {
					seen = append(seen, workflow.AIAuthoredFrom(ctx))
					return nil, false, nil
				})
				t.Cleanup(func() { workflow.SetExecGuard(nil) })
				_, _ = runPeople(t, &testConfig{DBPath: dbPath, ProfileID: "default"}, args...)
				if len(seen) != 1 || seen[0] != c.want {
					t.Errorf("%s: the guard saw an unapproved run = %v, want %v", args[1], seen, c.want)
				}
			}
		})
	}
}

// runToken records an approved, live execution in the database at dbPath and returns its token: what the
// engine hands the children of an approved workflow's node.
func runToken(t *testing.T, dbPath string, approved bool) string {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tok, err := workflow.IssueRunToken(context.Background(), db.DB, "exec-1", approved)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
