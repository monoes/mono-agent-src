package people

import (
	"context"
	"database/sql"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
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
		{"approved workflow's child", workflow.EnvWorkflowRun, "exec-1", false},
		{"chat's run", browserctl.EnvAICaller, browserctl.KindChat, true},
		{"operator terminal", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			asOperator(t)
			t.Setenv(workflow.EnvUnapprovedRun, "")
			if c.env != "" {
				t.Setenv(c.env, c.val)
			}
			if c.env == workflow.EnvUnapprovedRun {
				t.Setenv(workflow.EnvWorkflowRun, "exec-1") // every node's child has it too
			}
			dbPath := newMessagesCLITestDB(t)
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
