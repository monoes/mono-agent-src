package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// hilsend.Detect does not read MONOAGENT_WORKFLOW_RUN or MONOAGENT_AI_CALLER.
// `node run` decides for those: the chat's run, and a process a workflow node
// started in a run the user never approved (or one such a run called:
// workflow.EnvUnapprovedRun) are held like an agent's. A process a workflow
// the user approved started, and the operator's terminal, send at once.
func TestNodeRunOfASendFromAMarkedProcessWaits(t *testing.T) {
	cfg := map[string]interface{}{"chat_id": "42", "text": "Hi Ada"}
	cases := []struct {
		name, env, val string
		wantHeld       bool
	}{
		{"unapproved workflow's child", workflow.EnvUnapprovedRun, "1", true},
		{"unapproved run's child with its token", workflow.EnvRunToken, "unapproved", true},
		{"approved workflow's child that dropped the token", workflow.EnvWorkflowRun, "exec-1", true},
		{"approved workflow's child", workflow.EnvRunToken, "approved", false},
		{"chat's run", browserctl.EnvAICaller, browserctl.KindChat, true},
		{"operator terminal", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbPath := migratedTestDB(t) // clears the markers: set the caller up after it
			asTheOperator(t)
			for _, k := range []string{hilsend.EnvAgentContext, "MONOAGENT_ACTOR", hilsend.EnvApprovalID, hilsend.EnvApprovalProof, workflow.EnvUnapprovedRun, workflow.EnvRunToken} {
				t.Setenv(k, "")
			}
			if _, agent := hilsend.Detect(); agent {
				t.Fatal("test setup: Detect must see no agent")
			}
			if c.env != "" && c.env != workflow.EnvRunToken {
				t.Setenv(c.env, c.val)
			}
			if c.env == workflow.EnvUnapprovedRun || c.env == workflow.EnvRunToken {
				t.Setenv(workflow.EnvWorkflowRun, "exec-1") // every node's child has it too
			}
			switch c.val { // the engine hands a child the token of its run
			case "approved":
				t.Setenv(workflow.EnvRunToken, approvedRunToken(t, dbPath, true))
			case "unapproved":
				t.Setenv(workflow.EnvRunToken, approvedRunToken(t, dbPath, false))
			}
			var runs atomic.Int32
			d := testDeps(dbPath, "default", false)
			d.BuildRegistry = func(*sql.DB) *workflow.NodeTypeRegistry {
				workflow.SetExecGuard(hilsend.Guard) // as noderegistry.Build does
				r := workflow.NewNodeTypeRegistry()
				r.Register("comm.telegram", func() workflow.NodeExecutor { return sentNode{typ: "comm.telegram", runs: &runs} })
				return r
			}
			raw, _ := json.Marshal(map[string]interface{}{"config": cfg})
			out, err := runNodeSubcmdStdin(t, d, string(raw), "comm.telegram")
			if err != nil {
				t.Fatalf("node run: %v\n%s", err, out)
			}
			var res map[string][]map[string]map[string]interface{}
			_ = json.Unmarshal([]byte(out), &res)
			queued := len(res["main"]) == 1 && res["main"][0]["json"]["hil_id"] != nil
			if c.wantHeld && (!queued || runs.Load() != 0) {
				t.Errorf("the send was not held (runs %d): %s", runs.Load(), out)
			}
			if !c.wantHeld && (queued || runs.Load() != 1) {
				t.Errorf("the operator's send did not run at once (runs %d): %s", runs.Load(), out)
			}
		})
	}
}

// approvedRunToken records an approved, live execution in the database at dbPath and returns its token: what the
// engine hands the children of an approved workflow's node.
func approvedRunToken(t *testing.T, dbPath string, approved bool) string {
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
