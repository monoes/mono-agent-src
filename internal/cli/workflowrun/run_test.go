package workflowrun

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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
