package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/workflow"
)

// failingRunWorkflow builds and activates a workflow whose HTTP node asks a
// closed local port, so it fails when it runs.
func failingRunWorkflow(t *testing.T, cfg *globalConfig) string {
	t.Helper()
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "failing-run")), &created); err != nil {
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
	fetch := add("http.request", "Fetch", "--config", `{"url":"http://127.0.0.1:1/","method":"GET"}`)
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", trig, "--to", fetch)
	runWorkflowSubcmd(t, cfg, "activate", created.ID)
	return created.ID
}

// runLogsFor runs workflowID with the root logger initialised from args (as
// main does) and returns what it logged and printed.
func runLogsFor(t *testing.T, cfg *globalConfig, workflowID string, args ...string) (logs, stdout string, err error) {
	t.Helper()
	t.Setenv(logging.EnvLevel, "")
	prevDefault, prevOut, prevFlags, prevLevel := slog.Default(), log.Writer(), log.Flags(), logging.Level()
	var buf bytes.Buffer
	logging.Init(args, func(string) string { return "" }, &buf)
	t.Cleanup(func() {
		logging.Init(nil, func(string) string { return "" }, os.Stderr)
		slog.SetDefault(prevDefault)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		logging.SetLevel(prevLevel)
	})
	cmd := newWorkflowCmd(cfg)
	cmd.SetArgs([]string{"run", workflowID, "--timeout", "30s"})
	stdout = captureStdout(t, func() { err = cmd.Execute() })
	// Undo the run's demotion (a CLI process runs one command).
	logging.Demote(slog.LevelInfo, oneShotRunLogs...)()
	return buf.String(), stdout, err
}

// A human-mode run's summary reports a failed node; the engine's own log
// lines about it stay off the terminal unless a level was chosen.
func TestWorkflowRunHumanModeQuietsFailureLogs(t *testing.T) {
	_, cfg := persistTestEnv(t)
	id := failingRunWorkflow(t, cfg)
	cfg.JSONOutput = false

	logs, stdout, err := runLogsFor(t, cfg, id)
	if strings.Contains(logs, "level=") {
		t.Errorf("human-mode run logged:\n%s", logs)
	}
	if !strings.Contains(stdout, "Status: FAILED") || !strings.Contains(stdout, "Error:  ") {
		t.Errorf("summary does not report the failure:\n%s", stdout)
	}
	// The summary showed the error: main exits 1 without printing it again.
	var stdoutDoc, stderr bytes.Buffer
	reportCommandError([]string{"workflow", "run", id}, err, &stdoutDoc, &stderr)
	if err == nil || exitCodeFor(err) != 1 || stderr.Len() != 0 || stdoutDoc.Len() != 0 {
		t.Errorf("err %v (exit %d) reported as stderr %q stdout %q", err, exitCodeFor(err), stderr.String(), stdoutDoc.String())
	}

	logs, _, _ = runLogsFor(t, cfg, id, "--log-level=warn")
	if !strings.Contains(logs, `msg="node emitted error output`) || !strings.Contains(logs, `msg="engine: execution finished with error"`) {
		t.Errorf("--log-level=warn hid the node failure:\n%s", logs)
	}
}

// --json output is unchanged: the record on stdout, the engine's lines on
// stderr as before.
func TestWorkflowRunJSONKeepsFailureLogs(t *testing.T) {
	_, cfg := persistTestEnv(t)
	id := failingRunWorkflow(t, cfg)

	logs, stdout, err := runLogsFor(t, cfg, id)
	var rec struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil || rec.Status != "FAILED" {
		t.Errorf("run --json = %q (%v)", stdout, err)
	}
	if !strings.Contains(logs, `msg="node emitted error output`) {
		t.Errorf("--json run lost the engine's lines:\n%s", logs)
	}
	// --json reports the failure as before: the record above is the
	// document, the error goes to stderr, exit 1.
	var doc, stderr bytes.Buffer
	reportCommandError([]string{"--json", "workflow", "run", id}, err, &doc, &stderr)
	if errors.As(err, new(clicore.Shown)) || exitCodeFor(err) != 1 || !strings.Contains(stderr.String(), "failed: ") {
		t.Errorf("--json error %v: stdout %q stderr %q", err, doc.String(), stderr.String())
	}
}

// With --log-file the engine's lines go to the file at their own level, and
// none reach the terminal.
func TestWorkflowRunLogFileGetsFailureLogs(t *testing.T) {
	_, cfg := persistTestEnv(t)
	id := failingRunWorkflow(t, cfg)
	cfg.JSONOutput = false
	cfg.LogFile = filepath.Join(t.TempDir(), "run.log")

	logs, stdout, _ := runLogsFor(t, cfg, id, "--log-file", cfg.LogFile)
	if logs != "" {
		t.Errorf("terminal got logs with --log-file:\n%s", logs)
	}
	if !strings.Contains(stdout, "Status: FAILED") {
		t.Errorf("summary:\n%s", stdout)
	}
	data, err := os.ReadFile(cfg.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`level=ERROR msg="node emitted error output`, `level=WARN msg="engine: execution finished with error"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log file lacks %s:\n%s", want, data)
		}
	}
}

// Demotion matches the engine's messages by text: each must still be one.
func TestOneShotRunLogsAreEngineMessages(t *testing.T) {
	var src strings.Builder
	files, _ := filepath.Glob(filepath.Join("..", "..", "internal", "workflow", "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	for _, msg := range oneShotRunLogs {
		if !strings.Contains(src.String(), `Msg("`+msg+`")`) {
			t.Errorf("internal/workflow logs no %q — update oneShotRunLogs", msg)
		}
	}
}

// A wrapper that adds to a shown failure (a hint) is still printed.
func TestReportCommandErrorShownWithHint(t *testing.T) {
	shown := clicore.Shown{Err: errors.New("execution e1 failed: boom")}
	var stdout, stderr bytes.Buffer
	reportCommandError(nil, fmt.Errorf("%w — install it", shown), &stdout, &stderr)
	if !strings.Contains(stderr.String(), "boom — install it") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
