package summarycli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A run's error can hold a reply link. `summary` hides its token from a marked process and from one inside a
// coder turn that cleared its markers; the user's own terminal sees it. A marked caller's error is a literal,
// what it has always been given.
func TestSummaryHidesAReplyTokenFromAProcessInsideACoderTurn(t *testing.T) {
	const token = "Qm7WvXbNcRfLpHdGsKyJtZaEiUoYgBnMxCwDe_VkAr9"
	const link = "https://hooks.example.com/reply/" + token
	const hidden = "https://hooks.example.com/reply/" + workflow.ReplyTokenHidden
	cfg := newSummaryCLITestDB(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, error_message, created_at)
		VALUES ('e-fail','w1','FAILED','trigger.manual','default',?,'2026-09-26 10:00:00')`, "failed at "+link); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec)
		VALUES ('e-fail','ask',0,'w1',?,'approval','{"kind":"approval","outbound":true}')`, token); err != nil {
		t.Fatal(err)
	}
	db.Close()
	recentError := func() string {
		t.Helper()
		out, err := runSummary(t, cfg, "--section", "executions")
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Executions struct {
				Recent []struct{ Error string }
			}
		}
		if err := json.Unmarshal([]byte(out), &s); err != nil || len(s.Executions.Recent) != 1 {
			t.Fatalf("%v\n%s", err, out)
		}
		return s.Executions.Recent[0].Error
	}
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	if got := recentError(); got != "failed at "+link {
		t.Fatalf("the user's terminal: %q", got)
	}
	t.Setenv("MONOAGENT_AI_CALLER", "chat")
	if got := recentError(); got != "failed at "+hidden {
		t.Fatalf("a marked caller's output changed: %q", got)
	}
	t.Setenv("MONOAGENT_AI_CALLER", "")
	for name, why := range map[string]string{
		"in a turn":        "this command runs inside a coder session's turn, so a model is running it",
		"cannot be placed": operator.UnplacedPrefix + " (database unreadable)",
	} {
		operator.SetTurnVerdict(func() string { return why })
		if got := recentError(); got != "failed at "+hidden || strings.Contains(got, token) {
			t.Errorf("%s, markers cleared: %q", name, got)
		}
	}
	operator.SetTurnVerdict(nil)
}
