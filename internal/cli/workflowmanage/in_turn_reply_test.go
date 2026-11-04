package workflowmanage

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow executions` (listing, and --all) hides a reply link's token in a run's error from a marked
// process and from one inside a coder turn that cleared its markers; the user's terminal sees it. A marked
// caller's output is a literal, what it has always been given.
func TestWorkflowExecutionsHidesAReplyTokenFromAProcessInsideACoderTurn(t *testing.T) {
	const token = "Qm7WvXbNcRfLpHdGsKyJtZaEiUoYgBnMxCwDe_VkAr9"
	const link = "https://hooks.example.com/reply/" + token
	const hidden = "https://hooks.example.com/reply/" + workflow.ReplyTokenHidden
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default');
		INSERT INTO workflow_executions (id, workflow_id, status, profile_id, error_message, created_at) VALUES
		('e1','w1','FAILED','default','failed at `+link+`','2026-09-26 10:00:00');
		INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec)
		VALUES ('e1','ask',0,'w1','`+token+`','approval','{"kind":"approval","outbound":true}')`)
	views := map[string][]string{"list": {"executions", "w1"}, "all": {"executions", "--all"}}
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	viewers := []struct {
		name  string
		setup func()
		user  bool
	}{
		{"user", func() {}, true},
		{"marked chat", func() { t.Setenv("MONOAGENT_AI_CALLER", "chat") }, false},
		{"in a turn, markers cleared", func() {
			operator.SetTurnVerdict(func() string { return "this command runs inside a coder session's turn, so a model is running it" })
		}, false},
		{"cannot be placed", func() {
			operator.SetTurnVerdict(func() string { return operator.UnplacedPrefix + " (database unreadable)" })
		}, false},
	}
	for _, v := range viewers {
		for name, args := range views {
			t.Run(v.name+"/"+name, func(t *testing.T) {
				defer operator.SetTurnVerdict(nil)
				t.Setenv("MONOAGENT_AI_CALLER", "")
				v.setup()
				out, err := run(t, cfg, args...)
				if err != nil {
					t.Fatal(err)
				}
				if v.user != strings.Contains(out, token) || v.user != strings.Contains(out, "failed at "+link) || (!v.user && !strings.Contains(out, "failed at "+hidden)) {
					t.Errorf("user=%v:\n%s", v.user, out)
				}
			})
		}
	}
	// the redactor the commands use: nil only for the user
	operator.SetTurnVerdict(func() string { return "in a turn" })
	defer operator.SetTurnVerdict(nil)
	if replyRedactor(context.Background(), nil) == nil {
		t.Error("a process inside a coder turn got no redactor")
	}
}
