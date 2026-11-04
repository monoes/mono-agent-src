//go:build !windows

package workflowrun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A process inside a coder turn that cleared its markers is handled like a marked one by every view of a run
// that hides reply tokens and links (`workflow execution`: data, error and the waiting list; the execution
// summary `workflow run` prints; the redactor `run` and `retry` print through). What a marked caller gets is a
// fixed literal, the bytes it got before the turn check counted; the user's own terminal sees the tokens.

var inTurnReplyToken = "Qm7WvXbNcRfLpHdGsKyJtZaEiUoYgBnMxCwDe_VkAr9"

type replyViewer struct {
	name  string
	setup func(t *testing.T)
	user  bool
}

func replyViewers() []replyViewer {
	verdict := func(why string) func(*testing.T) {
		return func(t *testing.T) {
			operator.SetTurnVerdict(func() string { return why })
			t.Cleanup(func() { operator.SetTurnVerdict(nil) })
		}
	}
	return []replyViewer{
		{name: "user", setup: func(*testing.T) {}, user: true},
		{name: "marked chat", setup: func(t *testing.T) { t.Setenv("MONOAGENT_AI_CALLER", "chat") }},
		{name: "marked agent", setup: func(t *testing.T) { t.Setenv("CLAUDECODE", "1") }},
		{name: "in a turn, markers cleared", setup: verdict("this command runs inside a coder session's turn, so a model is running it")},
		{name: "cannot be placed", setup: verdict(operator.UnplacedPrefix + " (database unreadable)")},
	}
}

func seedReplyRun(t *testing.T) (*testConfig, string) {
	t.Helper()
	cfg, db := seedExecutionDB(t)
	t.Setenv("MONOAGENT_PUBLIC_URL", "https://hooks.example.com")
	link := "https://hooks.example.com/reply/" + inTurnReplyToken
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, error_message, created_at)
		VALUES ('e-turn','w1','WAITING','trigger.manual','default',?,'2026-09-26 10:00:00')`, "failed at "+link); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec)
		VALUES ('e-turn','ask',0,'w1',?,'approval','{"kind":"approval","outbound":true}')`, inTurnReplyToken); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_execution_nodes (id, execution_id, node_id, node_name, status, error_message, output_items, started_at)
		VALUES ('en-t','e-turn','n1','Keep','FAILED',?,?,'2026-09-26 10:00:01')`, "bad "+link, `[{"json":{"link":"`+link+`"}}]`); err != nil {
		t.Fatal(err)
	}
	return cfg, link
}

func TestWorkflowExecutionHidesReplyLinksFromAProcessInsideACoderTurn(t *testing.T) {
	const hidden = "https://hooks.example.com/reply/" + workflow.ReplyTokenHidden
	for _, v := range replyViewers() {
		t.Run(v.name, func(t *testing.T) {
			cfg, link := seedReplyRun(t)
			for _, k := range operator.MarkerEnv() {
				t.Setenv(k, "")
			}
			v.setup(t)
			cfg.JSONOutput = true
			out, err := runWorkflowCmdIn(t, cfg, "", "execution", "e-turn")
			if err != nil {
				t.Fatal(err)
			}
			var d executionDetailJSON
			if err := json.Unmarshal([]byte(out), &d); err != nil {
				t.Fatal(err)
			}
			if v.user {
				if !strings.Contains(out, inTurnReplyToken) || d.Error != "failed at "+link || len(d.Waiting) != 1 || d.Waiting[0].URL != link {
					t.Fatalf("the user's terminal lost a token or a link:\n%s", out)
				}
				return
			}
			// the marked caller's view, as literals
			if d.Error != "failed at "+hidden || len(d.Nodes) != 1 || d.Nodes[0].ErrorMessage != "bad "+hidden ||
				!strings.Contains(string(d.Nodes[0].OutputItems), hidden) {
				t.Errorf("error or data: %+v\n%s", d, out)
			}
			if len(d.Waiting) != 1 || d.Waiting[0].URL != "" || d.Waiting[0].Links != nil || !strings.Contains(d.Waiting[0].Note, "answering it lets a message to a person go out") {
				t.Errorf("waiting list: %+v", d.Waiting)
			}
			if strings.Contains(out, inTurnReplyToken) {
				t.Errorf("the token is in the output:\n%s", out)
			}
		})
	}
}

func TestExecutionSummaryAndRunRedactorHideReplyLinksFromAProcessInsideACoderTurn(t *testing.T) {
	const hidden = "https://hooks.example.com/reply/" + workflow.ReplyTokenHidden
	for _, v := range replyViewers() {
		t.Run(v.name, func(t *testing.T) {
			cfg, link := seedReplyRun(t)
			for _, k := range operator.MarkerEnv() {
				t.Setenv(k, "")
			}
			v.setup(t)
			d := testDeps(cfg)
			db, err := d.OpenDB()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s, err := executionSummaryFromStore(context.Background(), db, "e-turn")
			if err != nil {
				t.Fatal(err)
			}
			rr, done := d.replyRedactor(context.Background(), "e-turn")
			defer done()
			if v.user {
				if s.Error != "failed at "+link || rr != nil {
					t.Fatalf("the user's terminal: error %q, redactor nil: %v", s.Error, rr == nil)
				}
				return
			}
			if s.Error != "failed at "+hidden || rr == nil || rr.String(link) != hidden {
				t.Errorf("error %q, redactor nil: %v", s.Error, rr == nil)
			}
			if b, _ := json.Marshal(s.Nodes); strings.Contains(string(b), inTurnReplyToken) {
				t.Errorf("a node's data holds the token: %s", b)
			}
		})
	}
}
