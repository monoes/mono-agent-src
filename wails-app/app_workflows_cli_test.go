//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeWorkflowCLI is a monoagentcli stand-in answering the workflow
// subcommands the bindings use. It logs each argv to args.log and the
// stdin of `workflow save` to stdin.json.
func fakeWorkflowCLI(t *testing.T) (argsLog, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog, stdinLog = filepath.Join(dir, "args.log"), filepath.Join(dir, "stdin.json")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+argsLog+`'
case "$*" in
  *"workflow list"*) echo '[{"id":"w-old","name":"Old","updated_at":"2026-09-01T10:00:00Z","created_at":"2026-09-01T09:00:00.5Z","node_count":1},
    {"id":"w-new","name":"New","description":"d","is_active":true,"version":3,"profile_id":"work","updated_at":"2026-09-20T10:00:00Z","created_at":"2026-09-19T10:00:00Z","node_count":4}]' ;;
  *"workflow get w-inactive"*) echo '{"id":"w-inactive","name":"Dormant","is_active":false}' ;;
  *"workflow get missing"*) echo 'workflow "missing" not found' >&2; exit 2 ;;
  *"workflow get "*) echo '{"id":"w1","name":"A","is_active":true,"version":2,"created_at":"2026-09-19T10:00:00Z","updated_at":"2026-09-20T10:00:00Z",
    "nodes":[{"id":"n1","workflow_id":"w1","node_type":"trigger.manual","name":"Start","config":{"k":"v"},"position_x":1.5,"position_y":2,"disabled":true}],
    "connections":[{"id":"c1","workflow_id":"w1","source_node_id":"n1","source_handle":"main","target_node_id":"n2","target_handle":"main","position":1}]}' ;;
  *"workflow save"*) cat > '`+stdinLog+`'; echo '{"id":"w-saved","name":"S","is_active":true,"version":2,"profile_id":"work","created_at":"2026-09-19T10:00:00Z","updated_at":"2026-09-26T10:00:00.25Z"}' ;;
  *"workflow export"*) printf '{\n  "id": "w1",\n  "name": "A",\n  "nodes": []\n}\n' ;;
  *"workflow executions"*) echo '[{"id":"e1","workflow_id":"w1","profile_id":"work","status":"FAILED","trigger_type":"trigger.manual","trigger_data":{"token":"x"},
    "started_at":"2026-09-26T10:00:00Z","finished_at":null,"error_message":"boom","created_at":"2026-09-26T09:59:59.5Z"}]' ;;
  *"workflow execution "*) echo '{"id":"e1","workflow_id":"w1","status":"SUCCESS","trigger_type":"trigger.manual","started_at":"2026-09-26 10:00:00","finished_at":"","error_message":"halted","created_at":"2026-09-26T10:00:00Z",
    "nodes":[{"id":"en1","node_id":"n1","node_name":"Get","status":"SUCCESS","error_message":"","started_at":"s","finished_at":"f","input_items":"not items","output_items":[ {"json": {"api_key": "***"}} ],"retry_count":1}]}' ;;
  *"workflow run"*) echo "Execution started: e-run"; exec sleep 30 ;;
  *) echo '{}' ;;
esac
`))
	return argsLog, stdinLog
}

func workflowTestApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	a.selectForTest("work")
	return a
}

func wantCalls(t *testing.T, log string, want ...string) {
	t.Helper()
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWorkflowCRUDBindingsShellOut(t *testing.T) {
	log, stdin := fakeWorkflowCLI(t)
	a := workflowTestApp(t)

	list, err := a.ListWorkflows()
	if err != nil || len(list) != 2 {
		t.Fatalf("ListWorkflows = %+v, %v", list, err)
	}
	if n := list[0]; n.ID != "w-new" || !n.IsActive || n.Version != 3 || n.NodeCount != 4 || n.Description != "d" ||
		n.UpdatedAt != "2026-09-20T10:00:00Z" || list[1].CreatedAt != "2026-09-01T09:00:00Z" {
		t.Fatalf("ListWorkflows (newest first, RFC 3339) = %+v", list)
	}

	wf, err := a.GetWorkflow("w1")
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name != "A" || wf.Version != 2 || len(wf.Nodes) != 1 || len(wf.Connections) != 1 {
		t.Fatalf("GetWorkflow = %+v", wf)
	}
	if n := wf.Nodes[0]; n.NodeType != "trigger.manual" || n.PositionX != 1.5 || !n.Disabled || n.Config["k"] != "v" {
		t.Fatalf("node = %+v", n)
	}
	if c := wf.Connections[0]; c.SourceNodeID != "n1" || c.TargetHandle != "main" || c.Position != 1 {
		t.Fatalf("connection = %+v", c)
	}
	if _, err := a.GetWorkflow("missing"); err == nil || !strings.Contains(err.Error(), `workflow "missing" not found`) {
		t.Fatalf("GetWorkflow(missing) err = %v", err)
	}

	saved, err := a.SaveWorkflow(SaveWorkflowRequest{ID: "w-saved", Name: "S",
		Nodes:       []WorkflowNodeData{{ID: "n1", NodeType: "core.set", Name: "Set", PositionX: 3, Config: map[string]interface{}{"f": 1}}},
		Connections: []WorkflowConnectionData{{ID: "c1", SourceNodeID: "n1", TargetNodeID: "n2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != "w-saved" || !saved.IsActive || saved.Version != 2 || saved.UpdatedAt != "2026-09-26T10:00:00Z" {
		t.Fatalf("SaveWorkflow = %+v", saved)
	}
	raw, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ID    string `json:"id"`
		Nodes []struct {
			NodeType  string  `json:"node_type"`
			PositionX float64 `json:"position_x"`
		} `json:"nodes"`
		Connections []struct {
			SourceNodeID string `json:"source_node_id"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ID != "w-saved" || len(doc.Nodes) != 1 ||
		doc.Nodes[0].NodeType != "core.set" || doc.Nodes[0].PositionX != 3 || doc.Connections[0].SourceNodeID != "n1" {
		t.Fatalf("save stdin = %s (%v)", raw, err)
	}

	if err := a.DeleteWorkflow("w1"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetWorkflowActive("w1", true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetWorkflowActive("w1", false); err != nil {
		t.Fatal(err)
	}

	wantCalls(t, log,
		"--profile work --json workflow list",
		"--profile work --json workflow get w1",
		"--profile work --json workflow get missing",
		"--profile work --json workflow save",
		"--profile work --json workflow delete w1 --yes",
		"--profile work --json workflow activate w1",
		"--profile work --json workflow deactivate w1",
	)
}

func TestWorkflowExecutionBindingsShellOut(t *testing.T) {
	log, _ := fakeWorkflowCLI(t)
	a := workflowTestApp(t)
	a.ctx = context.Background() // polled bindings run under a deadline

	execs, err := a.GetWorkflowExecutions("w1", 0)
	if err != nil || len(execs) != 1 {
		t.Fatalf("GetWorkflowExecutions = %+v, %v", execs, err)
	}
	if e := execs[0]; e.ID != "e1" || e.Status != "FAILED" || e.Error != "boom" || e.StartedAt != "2026-09-26T10:00:00Z" ||
		e.FinishedAt != "" || e.CreatedAt != "2026-09-26T09:59:59.5Z" || e.TriggerType != "trigger.manual" {
		t.Fatalf("execution = %+v", e)
	}
	if _, err := a.GetWorkflowExecutions("w1", 3); err != nil {
		t.Fatal(err)
	}

	d, err := a.GetExecutionDetail("e1")
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok := d["nodes"].([]map[string]interface{})
	if d["id"] != "e1" || d["started_at"] != "2026-09-26 10:00:00" || d["error"] != "halted" || !ok || len(nodes) != 1 {
		t.Fatalf("GetExecutionDetail = %+v", d)
	}
	if nodes[0]["output_items"] != `[{"json":{"api_key":"***"}}]` || nodes[0]["input_items"] != "not items" || nodes[0]["retry_count"] != 1 {
		t.Fatalf("node = %+v", nodes[0])
	}

	wantCalls(t, log,
		"--profile work --json workflow executions w1 --limit 50",
		"--profile work --json workflow executions w1 --limit 3",
		"--profile work --json workflow execution e1",
	)
}

// A run checks the workflow through `workflow get`, then spawns `workflow
// run`. Cancelling it kills the process this app started and leaves the
// bookkeeping to `workflow cancel`.
func TestRunAndCancelWorkflowShellOut(t *testing.T) {
	log, _ := fakeWorkflowCLI(t)
	a := workflowTestApp(t)

	if err := a.RunWorkflowWithInput("w1", "[1,2]"); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("array input err = %v", err)
	}
	if err := a.RunWorkflow("w-inactive"); err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("inactive err = %v", err)
	}
	if err := a.RunWorkflow("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing err = %v", err)
	}
	if err := a.RunWorkflowWithInput("w1", ` {"prompt":"hi"} `); err != nil {
		t.Fatal(err)
	}
	var run *exec.Cmd
	for i := 0; i < 200 && run == nil; i++ {
		a.runningMu.Lock()
		run = a.runningCmds["e-run"]
		a.runningMu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if run == nil {
		t.Fatal("the run was never tracked by its execution id")
	}
	if err := a.RunWorkflow("w1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent run err = %v", err)
	}
	if err := a.CancelWorkflow("e-run"); err != nil {
		t.Fatal(err)
	}
	pid := run.Process.Pid
	for i := 0; i < 500 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("CancelWorkflow left the run's process alive")
	}
	a.runningMu.Lock()
	left := len(a.runningCmds)
	a.runningMu.Unlock()
	if left != 0 {
		t.Fatalf("runningCmds after cancel = %d entries", left)
	}

	wantCalls(t, log,
		"--profile work --json workflow get w-inactive",
		"--profile work --json workflow get missing",
		"--profile work --json workflow get w1",
		`--profile work workflow run w1 --input {"prompt":"hi"}`,
		"--profile work --json workflow get w1",
		"--profile work --json workflow cancel e-run",
	)
}

// A refused cancel (e.g. a pid the CLI would not signal) reaches the caller.
func TestCancelWorkflowReportsCLIRefusal(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo 'refusing to signal non-monoagent process (pid 7: sleep)' >&2; exit 1\n"))
	a := workflowTestApp(t)
	if err := a.CancelWorkflow("e1"); err == nil || !strings.Contains(err.Error(), "refusing to signal") {
		t.Fatalf("err = %v", err)
	}
}
