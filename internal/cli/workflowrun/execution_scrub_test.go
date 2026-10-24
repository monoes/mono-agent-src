//go:build !windows

package workflowrun

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
)

// HTTP and error output carries credentials under header names and inside text
// no key name catches. An agent's (a marked process's) `workflow execution`
// shows none of them; ordinary output is unchanged.
func TestWorkflowExecutionScrubsCredentialsForAnAgent(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	gh := "g" + "hp_abcdefghijklmnopqrstuvwxyz0123"
	live := "s" + "k_live_ABC123SECRETxyz"
	jwt := "e" + "yJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig_value-1"
	output := `[{"json":{"status":200,"headers":{"Set-Cookie":"sid=Zx9realcookievalue","X-Api-Key":"Zx9realapikeyvalue",` +
		`"Proxy-Authorization":"Basic Zx9realbasicvalue","Content-Type":"application/json"},` +
		`"body":"clone via ` + gh + ` now","usage":{"total_tokens":42},"title":"Quarterly report"}}]`
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, error_message, created_at)
		VALUES ('e-leak','w1','FAILED','trigger.manual','default',?,'2026-09-26 10:00:00')`,
		"HTTP 401 from https://api.x.com/v1?api_key="+live); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_execution_nodes (id, execution_id, node_id, node_name, status, error_message, input_items, output_items, started_at)
		VALUES ('en9','e-leak','n1','Call','FAILED',?,?,?,'2026-09-26 10:00:01')`,
		"retry with Bearer "+jwt, `{"Authorization":"Zx9rawauthvalue","note":"hello"}`, output); err != nil {
		t.Fatal(err)
	}
	cfg.JSONOutput = true
	secrets := []string{"Zx9realcookievalue", "Zx9realapikeyvalue", "Zx9realbasicvalue", gh[4:], live[8:], "sig_value-1", "Zx9rawauthvalue"}
	ordinary := []string{`"total_tokens": 42`, "Quarterly report", "application/json", `"status": 200`, "hello"}

	for _, mode := range []string{"agent", "agent-text"} {
		for _, k := range operator.MarkerEnv() {
			t.Setenv(k, "")
		}
		t.Setenv("CLAUDECODE", "1")
		args := []string{"execution", "e-leak"}
		if mode == "agent-text" {
			cfg.JSONOutput = false
		}
		out, err := runWorkflowCmdIn(t, cfg, "", args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s: %q reached the agent:\n%s", mode, s, out)
			}
		}
		if mode == "agent" {
			for _, o := range ordinary {
				if !strings.Contains(out, o) {
					t.Errorf("ordinary output %q was changed:\n%s", o, out)
				}
			}
		}
	}
	// The user's own terminal sees the run as it ran.
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	cfg.JSONOutput = true
	out, _ := runWorkflowCmdIn(t, cfg, "", "execution", "e-leak")
	if !strings.Contains(out, gh) {
		t.Errorf("the operator's output was scrubbed:\n%s", out)
	}
}
