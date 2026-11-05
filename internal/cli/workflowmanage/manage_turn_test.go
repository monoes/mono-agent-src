package workflowmanage

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A run's error can hold a reply's token. `workflow executions` (the list and one workflow's runs) hides
// it from a process that is not the user, and a process inside a coder session's turn is not the user
// although it carries no marker; a process the gate cannot place is hidden from too (issue 1425).
func TestWorkflowExecutionsHideReplyTokensFromAProcessInACoderTurnWithoutMarkers(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19730")
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	resume, err := workflow.ExecutionResumeURL("e-turn")
	if err != nil {
		t.Fatal(err)
	}
	token := resume[strings.LastIndex(resume, "/")+1:]
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default');
		INSERT INTO workflow_executions (id, workflow_id, status, profile_id, created_at, error_message) VALUES
		('e-turn','w1','WAITING','default','2026-09-26 10:00:00','failed after `+resume+`');
		INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec) VALUES
		('e-turn','wait',0,'w1','`+token+`','webhook','{"kind":"webhook","execution_url":true,"outbound":false}')`)
	views := [][]string{{"executions", "--all"}, {"executions", "w1"}}

	for _, args := range views {
		out, err := run(t, cfg, args...)
		if err != nil || !strings.Contains(out, token) {
			t.Fatalf("%v, the user's terminal must see the token: %v\n%s", args, err, out)
		}
	}
	for name, why := range map[string]string{
		"a coder turn":      "this process runs inside a coder turn",
		"an unplaced owner": opgate.UnplacedPrefix + "database is locked)",
	} {
		opgate.SetForTest(t, func(bool) string { return why })
		for _, args := range views {
			out, err := run(t, cfg, args...)
			if err != nil || strings.Contains(out, token) || !strings.Contains(out, "e-turn") {
				t.Errorf("%s, %v without markers: %v\n%s", name, args, err, out)
			}
		}
	}
}
