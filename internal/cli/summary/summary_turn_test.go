package summarycli

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A run's error can hold a reply's token. `summary` hides it from a process that is not the user, and
// a process inside a coder session's turn is not the user although it carries no marker (issue 1425).
func TestSummaryHidesReplyTokensFromAProcessInACoderTurnWithoutMarkers(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19728")
	resume, err := workflow.ExecutionResumeURL("e-turn")
	if err != nil {
		t.Fatal(err)
	}
	token := resume[strings.LastIndex(resume, "/")+1:]
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, created_at, error_message) VALUES
		('e-turn','w1','WAITING','trigger.manual','default',datetime('now'),?)`, "failed after "+resume); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec) VALUES
		('e-turn','wait',0,'w1',?,'webhook','{"kind":"webhook","execution_url":true,"outbound":false}')`, token); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}

	mine, err := runSummary(t, cfg, "--section", "executions")
	if err != nil || !strings.Contains(mine, token) {
		t.Fatalf("the user's terminal must see the token: %v\n%s", err, mine)
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	theirs, err := runSummary(t, cfg, "--section", "executions")
	if err != nil || strings.Contains(theirs, token) {
		t.Errorf("a coder turn without markers sees the token: %v\n%s", err, theirs)
	}
}
