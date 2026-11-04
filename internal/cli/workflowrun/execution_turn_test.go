//go:build !windows

package workflowrun

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A process inside a coder session's turn carries no marker once its chain was stripped of them, yet it
// is not the user: the reply tokens of a run's data stay hidden from it in `workflow execution`, in the
// run summary and in the redactor `workflow run` and `retry` use (issue 1425). The marker alone said "user".
func TestReplyTokensAreHiddenFromAProcessInACoderTurnWithoutMarkers(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19727")
	resume, err := workflow.ExecutionResumeURL("e-turn")
	if err != nil {
		t.Fatal(err)
	}
	token := resume[strings.LastIndex(resume, "/")+1:]
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, created_at, error_message) VALUES
		('e-turn','w1','WAITING','trigger.manual','default','2026-09-26 10:00:00',?)`, "failed after "+resume); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec) VALUES
		('e-turn','wait',0,'w1',?,'webhook','{"kind":"webhook","execution_url":true,"outbound":false}')`, token); err != nil {
		t.Fatal(err)
	}
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	ctx := context.Background()
	shown := func() (detail, summary string, rr *workflow.ReplyRedactor) {
		t.Helper()
		detail, err := runWorkflowCmdIn(t, cfg, "", "execution", "e-turn")
		if err != nil {
			t.Fatal(err)
		}
		s, err := executionSummaryFromStore(ctx, db, "e-turn")
		if err != nil {
			t.Fatal(err)
		}
		rr, done := testDeps(cfg).replyRedactor(ctx, "e-turn")
		done()
		return detail, s.Error, rr
	}

	detail, summary, rr := shown()
	if !strings.Contains(detail, token) || !strings.Contains(summary, token) || rr != nil {
		t.Fatalf("the user's terminal must see the token:\n%s\n%s\nredactor %v", detail, summary, rr)
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	detail, summary, rr = shown()
	if strings.Contains(detail, token) || strings.Contains(summary, token) || rr == nil {
		t.Errorf("a coder turn without markers sees the token:\n%s\n%s\nredactor %v", detail, summary, rr)
	}
}
