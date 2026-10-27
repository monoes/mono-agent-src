package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The chat builds and activates a workflow with a command step: until the
// user approves it, a run (the user's own `workflow run`, which doesn't
// approve, or its schedule) refuses that step and says how to approve the
// workflow. Once the user activates it, the same run runs the command.
func TestAWorkflowTheChatBuiltRunsNoCommandUntilApproved(t *testing.T) {
	touch, err := exec.LookPath("touch")
	if err != nil {
		t.Skip("uses touch")
	}
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	ran := filepath.Join(t.TempDir(), "ran")
	cmdCfg, _ := json.Marshal(map[string]interface{}{"command": touch, "args": []string{ran}})

	t.Setenv(browserctl.EnvAICaller, "chat") // the chat writes it
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "chat-built")), &created); err != nil {
		t.Fatal(err)
	}
	add := func(typ, name string, extra ...string) string {
		var n workflow.WorkflowNode
		args := append([]string{"node", "add", created.ID, "--type", typ, "--name", name}, extra...)
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, args...)), &n); err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	trig := add("trigger.manual", "Start")
	step := add("system.execute_command", "Touch", "--config", string(cmdCfg))
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", trig, "--to", step)
	activateUnapprovedForTest(t, cfg, created.ID)
	t.Setenv(browserctl.EnvAICaller, "")

	cfg.JSONOutput = false
	_, stdout, _ := runLogsFor(t, cfg, created.ID)
	if !strings.Contains(stdout, "runs a program on this computer") || !strings.Contains(stdout, "workflow activate "+created.ID) {
		t.Errorf("the unapproved run does not say why the step was refused:\n%s", stdout)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a workflow the chat built ran a command before the user approved it")
	}

	cfg.JSONOutput = true
	runWorkflowSubcmd(t, cfg, "activate", created.ID) // the user approves it
	cfg.JSONOutput = false
	if _, stdout, err := runLogsFor(t, cfg, created.ID); err != nil {
		t.Fatalf("the approved run: %v\n%s", err, stdout)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the approved workflow's command did not run: %v", err)
	}
}

// The chat builds and activates a workflow that writes ~/.bashrc: until the
// user approves it, the run refuses the write (a hidden file in the home
// runs code later) and says so; once the user activates it, it writes.
func TestAWorkflowTheChatBuiltWritesNoHiddenHomeFileUntilApproved(t *testing.T) {
	home, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	rc := filepath.Join(home, ".bashrc")
	stepCfg, _ := json.Marshal(map[string]interface{}{"file_path": rc, "field": "text", "encoding": "utf8"})
	setCfg, _ := json.Marshal(map[string]interface{}{"assignments": []map[string]interface{}{{"field": "text", "value": "echo hi"}}})

	t.Setenv(browserctl.EnvAICaller, "chat") // the chat writes and activates it
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "chat-built-rc")), &created); err != nil {
		t.Fatal(err)
	}
	add := func(typ, name string, extra ...string) string {
		var n workflow.WorkflowNode
		args := append([]string{"node", "add", created.ID, "--type", typ, "--name", name}, extra...)
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, args...)), &n); err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	trig := add("trigger.manual", "Start")
	set := add("core.set", "Text", "--config", string(setCfg))
	step := add("data.write_binary_file", "Write", "--config", string(stepCfg))
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", trig, "--to", set)
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", set, "--to", step)
	activateUnapprovedForTest(t, cfg, created.ID)
	t.Setenv(browserctl.EnvAICaller, "")

	cfg.JSONOutput = false
	_, stdout, _ := runLogsFor(t, cfg, created.ID)
	if !strings.Contains(stdout, "hidden file or folder") {
		t.Errorf("the unapproved run does not say why the write was refused:\n%s", stdout)
	}
	if _, err := os.Stat(rc); err == nil {
		t.Fatal("a workflow the chat built wrote ~/.bashrc before the user approved it")
	}
	cfg.JSONOutput = true
	runWorkflowSubcmd(t, cfg, "activate", created.ID) // the user approves it
	cfg.JSONOutput = false
	if _, stdout, err := runLogsFor(t, cfg, created.ID); err != nil {
		t.Fatalf("the approved run: %v\n%s", err, stdout)
	}
	if b, err := os.ReadFile(rc); err != nil || string(b) != "echo hi" {
		t.Fatalf("the approved workflow's write: %q, %v", b, err)
	}
}

// A workflow the chat built calls one the user approved that writes
// ~/.bashrc: the called workflow keeps to the caller's file rules, and the
// refusal names the caller to approve; once the user approves the caller,
// the same call writes (#831 round 6, S7).
func TestAWorkflowAnUnapprovedOneCallsKeepsToItsFileRules(t *testing.T) {
	home, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	rc := filepath.Join(home, ".bashrc")
	build := func(name string, nodes ...[2]string) string {
		t.Helper()
		var wf workflow.Workflow
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", name)), &wf); err != nil {
			t.Fatal(err)
		}
		prev := ""
		for _, n := range nodes {
			var node workflow.WorkflowNode
			if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", wf.ID, "--type", n[0], "--name", n[0], "--config", n[1])), &node); err != nil {
				t.Fatal(err)
			}
			if prev != "" {
				runWorkflowSubcmd(t, cfg, "connect", wf.ID, "--from", prev, "--to", node.ID)
			}
			prev = node.ID
		}
		return wf.ID
	}
	set, _ := json.Marshal(map[string]interface{}{"assignments": []map[string]interface{}{{"field": "text", "value": "echo hi"}}})
	write, _ := json.Marshal(map[string]interface{}{"file_path": rc, "field": "text", "encoding": "utf8"})
	child := build("writes-rc", [2]string{workflow.TriggerTypeExecuteWorkflow, `{}`},
		[2]string{"core.set", string(set)}, [2]string{"data.write_binary_file", string(write)})
	t.Setenv(browserctl.EnvAICaller, "chat") // the chat builds and activates the caller
	parent := build("calls-rc", [2]string{"trigger.manual", `{}`},
		[2]string{"core.execute_workflow", `{"workflowId":"` + child + `"}`})
	activateUnapprovedForTest(t, cfg, parent)
	t.Setenv(browserctl.EnvAICaller, "")

	cfg.JSONOutput = false
	_, out, _ := runLogsFor(t, cfg, parent)
	if _, err := os.Stat(rc); err == nil {
		t.Fatalf("a workflow the chat built had an approved one write ~/.bashrc:\n%s", out)
	}
	if !strings.Contains(out, "hidden file or folder") || !strings.Contains(out, "workflow activate "+parent) {
		t.Errorf("the refusal does not name the caller to approve:\n%s", out)
	}
	cfg.JSONOutput = true
	runWorkflowSubcmd(t, cfg, "activate", parent) // the user approves the caller
	cfg.JSONOutput = false
	if _, out, err := runLogsFor(t, cfg, parent); err != nil {
		t.Fatalf("the approved caller: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(rc); err != nil || string(b) != "echo hi" {
		t.Fatalf("the approved call's write: %q, %v", b, err)
	}
}

// `workflow run --input` can't plant the engine's own marks: the run's
// trigger data keeps the caller's fields and none of _ai_caller,
// _unapproved_caller and _subworkflow it was given, and its _items still
// feed the trigger its items.
func TestWorkflowRunInputCantPlantTheEnginesMarks(t *testing.T) {
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "plain")), &created); err != nil {
		t.Fatal(err)
	}
	var trig workflow.WorkflowNode
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", "trigger.manual", "--name", "Start")), &trig); err != nil {
		t.Fatal(err)
	}
	runWorkflowSubcmd(t, cfg, "activate", created.ID)
	cmd := newWorkflowCmd(cfg)
	cmd.SetArgs([]string{"run", created.ID, "--timeout", "30s", "--input",
		`{"keep":"yes","_ai_caller":"unapproved-workflow","_unapproved_caller":"wf-x","_items":[{"n":1},{"n":2}],"_subworkflow":{}}`})
	captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw, out string
	if err := db.DB.QueryRow(`SELECT e.trigger_data, n.output_items FROM workflow_executions e
		JOIN workflow_execution_nodes n ON n.execution_id = e.id AND n.node_id = ?
		WHERE e.workflow_id = ? ORDER BY e.rowid DESC LIMIT 1`, trig.ID, created.ID).Scan(&raw, &out); err != nil {
		t.Fatal(err)
	}
	var td map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		t.Fatalf("trigger_data %q: %v", raw, err)
	}
	if td["keep"] != "yes" {
		t.Errorf("the caller's own field is gone: %v", td)
	}
	for _, k := range []string{"_ai_caller", "_unapproved_caller", "_subworkflow"} {
		if _, ok := td[k]; ok {
			t.Errorf("--input planted %s: %v", k, td)
		}
	}
	// _items is the caller's way to pass several items, and still feeds
	// them to the trigger (#833 review).
	var items []workflow.Item
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("the trigger's output %q: %v", out, err)
	}
	if len(items) != 2 || items[0].JSON["n"] != float64(1) || items[1].JSON["n"] != float64(2) {
		t.Errorf("--input _items fed %v, want two items", items)
	}
}

// An agent's `workflow run` keeps the mark that says an agent started it:
// stripping a run's input and an event's payload leaves the engine's own
// marks alone (#836 review). A sub-workflow keeps its caller's marks the
// same way (TestAnUnapprovedWorkflowsSubWorkflowIsAnAIRun).
func TestAnAgentsWorkflowRunKeepsItsMark(t *testing.T) {
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "plain")), &created); err != nil {
		t.Fatal(err)
	}
	runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", "trigger.manual", "--name", "Start")
	runWorkflowSubcmd(t, cfg, "activate", created.ID)

	asAgentProcess(t)
	cmd := newWorkflowCmd(cfg)
	cmd.SetArgs([]string{"run", created.ID, "--timeout", "30s", "--input", `{"_ai_caller":"chat"}`})
	captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.DB.QueryRow(`SELECT trigger_data FROM workflow_executions WHERE workflow_id = ? ORDER BY rowid DESC LIMIT 1`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var td map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		t.Fatal(err)
	}
	if mark, _ := td[workflow.AICallerKey].(string); mark == "" || mark == "chat" {
		t.Fatalf("an agent's run is marked %q, want its own kind: %v", mark, td)
	}
}
