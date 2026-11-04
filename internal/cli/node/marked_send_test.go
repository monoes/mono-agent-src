package node

import (
	"database/sql"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/workflow"
)

// hilsend.Detect does not read MONOAGENT_WORKFLOW_RUN or MONOAGENT_AI_CALLER
// on purpose: `node run` takes one verdict on who runs it (opgate.Refusal,
// operator.Marker's list, those two included) and, for anything but the
// user's terminal, runs the node as an unapproved step, which hilsend.Guard
// holds like an agent's. A process a workflow node started, or the chat's
// run, therefore cannot send a message to a person without an approval,
// while the operator's terminal still sends at once.
func TestNodeRunOfASendFromAMarkedProcessWaits(t *testing.T) {
	cfg := map[string]interface{}{"chat_id": "42", "text": "Hi Ada"}
	cases := []struct {
		name, env, val string
		wantHeld       bool
	}{
		{"workflow node's child", workflow.EnvWorkflowRun, "exec-1", true},
		{"chat's run", browserctl.EnvAICaller, browserctl.KindChat, true},
		{"operator terminal", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbPath := migratedTestDB(t) // clears the markers: set the caller up after it
			asTheOperator(t)
			for _, k := range []string{hilsend.EnvAgentContext, "MONOAGENT_ACTOR", hilsend.EnvApprovalID, hilsend.EnvApprovalProof} {
				t.Setenv(k, "")
			}
			if _, agent := hilsend.Detect(); agent {
				t.Fatal("test setup: Detect must see no agent")
			}
			if c.env != "" {
				t.Setenv(c.env, c.val)
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
