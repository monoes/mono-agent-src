package workflowrun

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/nodes/browserjev"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runDB is a migrated database with workflow "w" (trigger.manual → core.set), active, and
// "off", the same but inactive.
func runDB(t *testing.T, jsonOut bool) *testConfig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "r.db"), JSONOutput: jsonOut, ProfileID: "default"}
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	for _, id := range []string{"w", "off"} {
		wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default", Nodes: []workflow.WorkflowNode{
			{ID: id + "-t", WorkflowID: id, Type: "trigger.manual", Name: "Start"},
			{ID: id + "-s", WorkflowID: id, Type: "core.set", Name: "Greet", Config: map[string]interface{}{
				"greeting": "hello {{ $json.who }}", "include_input": false}}},
			Connections: []workflow.WorkflowConnection{{ID: id + "-c", WorkflowID: id, SourceNodeID: id + "-t", SourceHandle: "main",
				TargetNodeID: id + "-s", TargetHandle: "main"}}}
		for i := range wf.Nodes {
			if err := wf.Nodes[i].MarshalConfig(); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.CreateWorkflow(ctx, wf); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowNodes(ctx, id, wf.Nodes); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowConnections(ctx, id, wf.Connections); err != nil {
			t.Fatal(err)
		}
		if err := store.SetWorkflowActive(ctx, id, id == "w"); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// `workflow run --json` runs the workflow with the core nodes and prints one record: status,
// and every node's output items.
func TestRunJSONRecord(t *testing.T) {
	cfg := runDB(t, true)
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--input", `{"who":"Ada"}`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var rec executionSummaryJSON
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rec.Status != "SUCCESS" || rec.WorkflowID != "w" || len(rec.Nodes) != 2 || rec.StartedAt == nil {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Nodes[1].Type != "core.set" || rec.Nodes[1].Status != "SUCCESS" || len(rec.Nodes[1].OutputItems) != 1 {
		t.Fatalf("set node = %+v", rec.Nodes[1])
	}
	if got := rec.Nodes[1].OutputItems[0].JSON["greeting"]; got != "hello Ada" {
		t.Errorf("greeting = %v", got)
	}

	// The execution is there for `workflow execution`.
	detail, err := runWorkflowCmdIn(t, cfg, "", "execution", rec.ExecutionID)
	if err != nil || !strings.Contains(detail, `"status": "SUCCESS"`) {
		t.Fatalf("execution: %v\n%s", err, detail)
	}
}

// Without --json the run says it started, its status, and the output of its last node.
func TestRunText(t *testing.T) {
	cfg := runDB(t, false)
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--input", `{"who":"Ada"}`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"Execution started: ", "Status: SUCCESS\n", "\nOutput (Greet):\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The refusals before anything runs: bad --input, an inactive or unknown workflow.
func TestRunRefusals(t *testing.T) {
	cfg := runDB(t, true)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"run", "w", "--input", "[1]"}, 3, "invalid --input JSON"},
		{[]string{"run", "off"}, 3, "activate it first: monoagentcli workflow activate off"},
		{[]string{"run", "nope"}, 2, `workflow "nope" not found — list them with: monoagentcli workflow list`},
		{[]string{"execution", "nope"}, 2, `execution "nope" not found`},
		{[]string{"cancel", "nope"}, 2, `execution "nope" not found`},
		{[]string{"templates", "run", "no-such-template"}, 1, `unknown template "no-such-template"`},
	}
	for _, c := range cases {
		out, err := runWorkflowCmdIn(t, cfg, "", c.args...)
		if err == nil || exitCode(err) != c.code || !strings.Contains(err.Error(), c.want) || out != "" {
			t.Errorf("%v: err %v (exit %d), stdout %q; want exit %d, %q", c.args, err, exitCode(err), out, c.code, c.want)
		}
	}
}

// --no-wait queues the run for a live engine and prints the short record; --dry-run is the
// CLI's DryRun.
func TestRunNoWaitAndDryRun(t *testing.T) {
	cfg := runDB(t, true)
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]string
	if err := json.Unmarshal([]byte(out), &rec); err != nil || rec["status"] != "QUEUED" || rec["workflow_id"] != "w" ||
		rec["hint"] != noWaitHint || rec["execution_id"] == "" {
		t.Fatalf("no-wait = %q (%v)", out, err)
	}

	dryRuns = nil
	if _, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--dry-run"); err == nil || err.Error() != "dry run" ||
		len(dryRuns) != 1 || dryRuns[0] != "w" {
		t.Fatalf("dry run: %v, %v", err, dryRuns)
	}
}

func TestRunCmdFlags(t *testing.T) {
	t.Parallel()
	d := testDeps(&testConfig{})
	for cmd, flags := range map[string][]string{
		d.NewRunCmd().Name():                         {"input", "no-wait", "timeout", "dry-run"},
		"templates " + d.NewTemplatesRunCmd().Name(): {"input", "keep"},
	} {
		c := d.NewRunCmd()
		if strings.HasPrefix(cmd, "templates") {
			c = d.NewTemplatesRunCmd()
		}
		for _, f := range flags {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("%s lacks --%s", cmd, f)
			}
		}
	}
	if d.NewExecutionCmd().Use != "execution <execution-id>" || d.NewCancelCmd().Use != "cancel <execution-id>" {
		t.Error("execution/cancel Use changed")
	}
}

// A wait ended by the command's context (workflow cancel's SIGTERM) reports
// a cancelled run and still has a live context to read its record with.
func TestStoppedWaitReportsCancelledRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sctx, scancel, err := stoppedWait(ctx, "e1", nil)
	scancel()
	if sctx != ctx || err != nil {
		t.Fatalf("live context: got %v, %v", sctx, err)
	}
	cancel()
	sctx, scancel, err = stoppedWait(ctx, "e1", context.Canceled)
	defer scancel()
	if sctx.Err() != nil {
		t.Errorf("summary context is done: %v", sctx.Err())
	}
	if err == nil || err.Error() != "execution e1 was cancelled" {
		t.Errorf("err = %v, want the run reported cancelled", err)
	}
}

// A run an agent starts carries that to the engine that runs it: the
// daemon adopting a --no-wait run gates browser.jev's submits too.
func TestRunMarksAnAgentsRun(t *testing.T) {
	cfg := runDB(t, true)
	for _, k := range append(browserctl.AgentContextEnv(), browserjev.EnvAICaller, browserctl.EnvAgentKind) {
		t.Setenv(k, "")
	}
	mark := func() interface{} {
		t.Helper()
		out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--no-wait", "--input", `{"who":"zed"}`)
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]string
		if err := json.Unmarshal([]byte(out), &rec); err != nil {
			t.Fatal(err)
		}
		db, err := openTestDB(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var raw string
		if err := db.DB.QueryRow(`SELECT trigger_data FROM workflow_executions WHERE id = ?`, rec["execution_id"]).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &data); err != nil || data["who"] != "zed" {
			t.Fatalf("trigger data %s: %v", raw, err)
		}
		return data[workflow.AICallerKey]
	}
	if got := mark(); got != nil {
		t.Errorf("the user's run is not marked: %v", got)
	}
	t.Setenv(browserjev.EnvAICaller, "chat")
	if got := mark(); got != "chat" {
		t.Errorf("a chat tool call's run: %v", got)
	}
	t.Setenv("CLAUDECODE", "1")
	if got := mark(); got != "other" {
		t.Errorf("an agent shell's run (even with the chat's variable set) is marked with its kind: %v", got)
	}
	browserctltest.AsAgentKind(t, browserctl.KindCoder) // what a coder turn's spawner gives its shell
	if got := mark(); got != "coder" {
		t.Errorf("a coder chat's run: %v", got)
	}
}

// A process inside a coder turn that cleared its markers is still an agent: its `workflow run` is
// marked as an agent's run (browser level, RunRefusal, AIRun and the send queue follow from the mark),
// where the marker alone called it the user's. The user's own terminal outside a turn stays unmarked.
func TestRunMarksARunOfAProcessInACoderTurnWithoutMarkers(t *testing.T) {
	cfg := runDB(t, true)
	for _, k := range append(browserctl.AgentContextEnv(), browserjev.EnvAICaller, browserctl.EnvAgentKind) {
		t.Setenv(k, "")
	}
	mark := func() interface{} {
		t.Helper()
		out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--no-wait", "--input", `{"who":"zed"}`)
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]string
		if err := json.Unmarshal([]byte(out), &rec); err != nil {
			t.Fatal(err)
		}
		db, err := openTestDB(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var raw string
		if err := db.DB.QueryRow(`SELECT trigger_data FROM workflow_executions WHERE id = ?`, rec["execution_id"]).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		return data[workflow.AICallerKey]
	}
	opgate.SetForTest(t, func(bool) string { return "" })
	if got := mark(); got != nil {
		t.Errorf("the user's terminal outside a turn is not marked: %v", got)
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	if got := mark(); got != browserctl.KindOther {
		t.Errorf("a coder turn without markers is marked as an agent's run: %v", got)
	}
	t.Setenv("CLAUDECODE", "1")
	browserctltest.AsAgentKind(t, browserctl.KindCoder)
	if got := mark(); got != browserctl.KindCoder {
		t.Errorf("a marked coder keeps its own kind: %v", got)
	}
}

// runMarkOf starts `workflow run --no-wait` and returns the AI-caller mark its execution stored (nil: none).
func runMarkOf(t *testing.T, cfg *testConfig) interface{} {
	t.Helper()
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--no-wait", "--input", `{"who":"zed"}`)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]string
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatal(err)
	}
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.DB.QueryRow(`SELECT trigger_data FROM workflow_executions WHERE id = ?`, rec["execution_id"]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	return data[workflow.AICallerKey]
}

func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), browserjev.EnvAICaller, browserctl.EnvAgentKind, workflow.EnvWorkflowRun, workflow.EnvUnapprovedRun, workflow.EnvRunToken) {
		t.Setenv(k, "")
	}
}

// A bare MONOAGENT_AI_CALLER=chat does not buy a turn the chat's browser level: a process the turn check
// places in a recorded turn is marked other (the stricter of the two), a genuine chat process outside a turn keeps chat.
func TestRunDoesNotHonourABareChatClaimInACoderTurn(t *testing.T) {
	cfg := runDB(t, true)
	clearAgentEnv(t)
	t.Setenv(browserjev.EnvAICaller, "chat")
	opgate.SetForTest(t, func(bool) string { return "MONOAGENT_AI_CALLER is set" })
	prev := testInTurn
	t.Cleanup(func() { testInTurn = prev })
	testInTurn = func() (bool, error) { return false, nil }
	if got := runMarkOf(t, cfg); got != browserctl.KindChat {
		t.Errorf("a genuine chat process outside a turn: %v, want chat", got)
	}
	testInTurn = func() (bool, error) { return true, nil }
	if got := runMarkOf(t, cfg); got != browserctl.KindOther {
		t.Errorf("a chat claim inside a turn: %v, want other", got)
	}
}

// A node of an approved workflow that runs `workflow run` (EnvWorkflowRun and the approved run's token, no other
// marker) starts an agent's run: marked other, so its sends queue and non-builtin browser packages are refused.
// One that shows no approved token (dropped, or the run was unapproved) is marked as called by an unapproved workflow.
func TestRunMarksTheChildRunOfAnApprovedWorkflowOther(t *testing.T) {
	cfg := runDB(t, true)
	clearAgentEnv(t)
	t.Setenv(workflow.EnvWorkflowRun, "run-marker")
	if got := runMarkOf(t, cfg); got != workflow.UnapprovedWorkflowCaller {
		t.Errorf("a workflow node's child run with no token: %v, want %s", got, workflow.UnapprovedWorkflowCaller)
	}
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tok, err := workflow.IssueRunToken(context.Background(), db.DB, "run-marker", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(workflow.EnvRunToken, tok)
	if got := runMarkOf(t, cfg); got != browserctl.KindOther {
		t.Errorf("a workflow node's child run with its approved token: %v, want other", got)
	}
	bad, err := workflow.IssueRunToken(context.Background(), db.DB, "run-bad", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(workflow.EnvRunToken, bad)
	if got := runMarkOf(t, cfg); got != workflow.UnapprovedWorkflowCaller {
		t.Errorf("a child of an unapproved run: %v, want %s", got, workflow.UnapprovedWorkflowCaller)
	}
}

// A process the turn check cannot place is marked other, and the command says so once on stderr.
func TestRunSaysOnStderrWhenTheProcessCannotBePlaced(t *testing.T) {
	cfg := runDB(t, true)
	clearAgentEnv(t)
	opgate.SetForTest(t, func(bool) string { return operator.UnplacedPrefix + " (database is locked)" })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	mark := runMarkOf(t, cfg)
	os.Stderr = orig
	w.Close()
	buf, _ := io.ReadAll(r)
	if mark != browserctl.KindOther {
		t.Errorf("an unplaceable process: %v, want other", mark)
	}
	if n := strings.Count(string(buf), "marked as an agent's run"); n != 1 {
		t.Errorf("stderr has the unplaced line %d times, want once:\n%s", n, buf)
	}
}
