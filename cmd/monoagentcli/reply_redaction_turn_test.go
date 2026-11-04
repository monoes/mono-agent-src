package main

import (
	"database/sql"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Reply tokens and links are hidden from every process the gate would limit, not only from one that carries a
// marker: through the production gate, a process inside a coder turn that cleared its markers (also one in a
// turn of a registered host, one holding an agent token, one the gate could not place) gets the redacted views
// a marked agent gets, from `hil list`, `summary`, `workflow execution` (data, error, waiting links) and
// `workflow executions`, and from the process's own log lines; the user's terminal sees them.

const (
	redactToken = "Qm7WvXbNcRfLpHdGsKyJtZaEiUoYgBnMxCwDe_VkAr9"
	redactLink  = "https://hooks.example.com/reply/" + redactToken
)

func redactionFixture(t *testing.T) *globalConfig {
	t.Helper()
	cfg := sendEnv(t)
	t.Setenv("MONOAGENT_PUBLIC_URL", "https://hooks.example.com")
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default')`, nil},
		{`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, error_message, created_at)
			VALUES ('e-fail','w1','FAILED','trigger.manual','default',?,'2026-09-26 10:00:00')`, []interface{}{"failed at " + redactLink}},
		{`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, created_at)
			VALUES ('e-wait','w1','WAITING','trigger.manual','default','2026-09-26 10:01:00')`, nil},
		{`INSERT INTO workflow_execution_nodes (id, execution_id, node_id, node_name, status, output_items, started_at)
			VALUES ('en1','e-wait','n1','Keep','SUCCESS',?,'2026-09-26 10:01:01')`, []interface{}{`[{"json":{"link":"` + redactLink + `"}}]`}},
		{`INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec)
			VALUES ('e-wait','ask',0,'w1',?,'approval','{"kind":"approval","outbound":true}')`, []interface{}{redactToken}},
		{`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id, created_at)
			VALUES ('h1','e-wait','w1','n','Review','pending',?,'{}','{"outbound":false}','default','2026-09-26 10:02:00')`, []interface{}{`{"link":"` + redactLink + `"}`}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

type redactionView struct {
	name string
	run  func(t *testing.T, cfg *globalConfig) string
}

type cobraRunner interface {
	SetArgs([]string)
	Execute() error
}

func redactionViews() []redactionView {
	view := func(name string, build func(*globalConfig) cobraRunner, args ...string) redactionView {
		return redactionView{name, func(t *testing.T, cfg *globalConfig) string {
			t.Helper()
			out, err := runSendCLI(t, cfg, build(cfg), args...)
			if err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			return out
		}}
	}
	hil := func(c *globalConfig) cobraRunner { return newHILCmd(c) }
	summary := func(c *globalConfig) cobraRunner { return newSummaryCmd(c) }
	wf := func(c *globalConfig) cobraRunner { return newWorkflowCmd(c) }
	return []redactionView{
		view("hil list", hil, "list"),
		view("summary", summary, "--section", "executions"),
		view("workflow execution (data and error)", wf, "execution", "e-fail"),
		view("workflow execution (waiting links)", wf, "execution", "e-wait"),
		view("workflow executions", wf, "executions", "w1"),
	}
}

func TestReplyTokensAreHiddenFromEveryProcessTheGateLimits(t *testing.T) {
	contexts := append([]revealGateContext{{name: "operator", setup: func(*testing.T, *globalConfig) {}}}, revealGateContexts()...)
	for _, v := range redactionViews() {
		t.Run(v.name, func(t *testing.T) {
			// what a marked agent gets: every other limited flavour must get the same bytes
			cfg := redactionFixture(t)
			opgate.SetForTest(t, cfg.gateRefusal)
			t.Setenv("CLAUDECODE", "1")
			marked := v.run(t, cfg)
			if strings.Contains(marked, redactToken) {
				t.Fatalf("a marked agent sees the token:\n%s", marked)
			}
			for _, c := range contexts {
				t.Run(c.name, func(t *testing.T) {
					cfg := redactionFixture(t)
					opgate.SetForTest(t, cfg.gateRefusal)
					c.setup(t, cfg)
					out := v.run(t, cfg)
					if c.name == "operator" {
						if !strings.Contains(out, redactToken) {
							t.Fatalf("the user's terminal lost the token or link:\n%s", out)
						}
						return
					}
					if strings.Contains(out, redactToken) {
						t.Fatalf("the token reached a limited process (%s):\n%s", c.name, out)
					}
					if normalizeRedactionView(out) != normalizeRedactionView(marked) {
						t.Fatalf("%s: not what a marked agent gets\n got: %s\nwant: %s", c.name, out, marked)
					}
				})
			}
		})
	}
}

// normalizeRedactionView drops the one field that differs between two runs of the same fixture.
func normalizeRedactionView(s string) string {
	return generatedAt.ReplaceAllString(s, `"generated_at":""`)
}

var generatedAt = regexp.MustCompile(`"generated_at":\s*"[^"]*"`)

// The log filter of the process: a marked process hides tokens at once; any other asks the turn check once,
// at its first line once the gate is registered, and keeps the answer; one it cannot place hides.
func TestLogFilterHidesReplyTokensFromEveryProcessTheGateLimits(t *testing.T) {
	line := "node failed: GET " + redactLink + "\n"
	hiddenLine := "node failed: GET https://hooks.example.com/reply/" + workflow.ReplyTokenHidden + "\n"
	contexts := append([]revealGateContext{{name: "operator", setup: func(*testing.T, *globalConfig) {}}}, revealGateContexts()...)
	for _, c := range contexts {
		t.Run(c.name, func(t *testing.T) {
			cfg := redactionFixture(t)
			opgate.SetForTest(t, cfg.gateRefusal)
			c.setup(t, cfg)
			f := agentLogFilter()
			for i := 0; i < 3; i++ {
				got := line
				if f != nil {
					got = f(line)
				}
				if c.name == "operator" {
					if got != line {
						t.Fatalf("the user's terminal: %q", got)
					}
					continue
				}
				if got != hiddenLine {
					t.Fatalf("%s, line %d: %q", c.name, i, got)
				}
			}
		})
	}
}

// A marked process opens no database for its log filter; the others ask once however many lines they write; a
// check that logs while it runs, or panics, fails closed; before the gate is registered nothing is decided.
func TestLogFilterAsksTheTurnCheckOnceAndFailsClosed(t *testing.T) {
	line := "x " + redactLink + "\n"
	hiddenLine := "x https://hooks.example.com/reply/" + workflow.ReplyTokenHidden + "\n"
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	calls := atomic.Int32{}
	count := func(why string, f func()) {
		operator.SetTurnVerdict(func() string {
			calls.Add(1)
			if f != nil {
				f()
			}
			return why
		})
		t.Cleanup(func() { operator.SetTurnVerdict(nil) })
	}

	t.Run("marked: no check at all", func(t *testing.T) {
		calls.Store(0)
		t.Setenv("CLAUDECODE", "1")
		count("", nil)
		f := agentLogFilter()
		if f(line) != hiddenLine || f(line) != hiddenLine || calls.Load() != 0 {
			t.Fatalf("calls = %d", calls.Load())
		}
	})
	t.Run("the user: asked once", func(t *testing.T) {
		calls.Store(0)
		count("", nil)
		f := agentLogFilter()
		for i := 0; i < 50; i++ {
			if f(line) != line {
				t.Fatal("the user's line changed")
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("the turn check ran %d times", calls.Load())
		}
	})
	t.Run("in a turn: asked once", func(t *testing.T) {
		calls.Store(0)
		count("this command runs inside a coder session's turn, so a model is running it", nil)
		f := agentLogFilter()
		for i := 0; i < 50; i++ {
			if f(line) != hiddenLine {
				t.Fatal("a turn's line kept the token")
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("the turn check ran %d times", calls.Load())
		}
	})
	t.Run("a check that logs through the filter does not recurse, and the line fails closed", func(t *testing.T) {
		calls.Store(0)
		var f func(string) string
		var inner string
		count("", func() { inner = f(line) })
		f = agentLogFilter()
		if got := f(line); got != line || inner != hiddenLine || calls.Load() != 1 {
			t.Fatalf("outer %q inner %q calls %d", got, inner, calls.Load())
		}
	})
	t.Run("a check that panics hides", func(t *testing.T) {
		calls.Store(0)
		count("", func() { panic("boom") })
		f := agentLogFilter()
		if f(line) != hiddenLine || f(line) != hiddenLine || calls.Load() != 1 {
			t.Fatalf("calls = %d", calls.Load())
		}
	})
	t.Run("before the gate is registered nothing is decided, after it the first line is", func(t *testing.T) {
		calls.Store(0)
		count("this command runs inside a coder session's turn, so a model is running it", nil)
		prev := inTestBinary
		inTestBinary = func() bool { return false }
		logGateRegistered.Store(false)
		t.Cleanup(func() { inTestBinary = prev; logGateRegistered.Store(false) })
		f := agentLogFilter()
		if f(line) != line || calls.Load() != 0 {
			t.Fatalf("a line before the gate: calls = %d", calls.Load())
		}
		logGateRegistered.Store(true)
		if f(line) != hiddenLine || f(line) != hiddenLine || calls.Load() != 1 {
			t.Fatalf("after the gate: calls = %d", calls.Load())
		}
	})
}
