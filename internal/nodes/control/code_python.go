package control

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/nodemgr"
	"github.com/monoes/mono-agent/internal/workflow"
)

// pythonBootstrap gives the code n8n's Python names and talks JSON with
// runPython over stdin/stdout.
//
//go:embed code_python.py
var pythonBootstrap string

// pythonEnvVar names the Python interpreter core.code runs Python with,
// in place of python3 from the user's PATH.
const pythonEnvVar = "MONOAGENT_PYTHON"

// pythonModes are n8n's Code node modes; Python runs the code once for
// all items unless the config's mode says once per item.
const (
	pythonAllItems = "runOnceForAllItems"
	pythonEachItem = "runOnceForEachItem"
)

// findPython resolves MONOAGENT_PYTHON, else python3 (then python) on the
// user's PATH.
func findPython() (string, error) {
	if p := strings.TrimSpace(os.Getenv(pythonEnvVar)); p != "" {
		bin, err := nodemgr.LookPathUser(p)
		if err != nil {
			return "", fmt.Errorf("%s=%s: %w", pythonEnvVar, p, err)
		}
		return bin, nil
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := nodemgr.LookPathUser(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("python3 not found (set " + pythonEnvVar + ")")
}

// runPython runs code with the system Python as n8n's Code node runs
// Python: once for all items (_items, return a list of dicts) or once per
// item (_item, return a dict). Errors carry the code's traceback lines.
func runPython(ctx context.Context, input workflow.NodeInput, config map[string]interface{}, code string, timeout time.Duration) ([]workflow.NodeOutput, error) {
	bin, err := findPython()
	if err != nil {
		return nil, fmt.Errorf("code node: %w", err)
	}
	mode, _ := config["mode"].(string)
	if mode != pythonEachItem {
		mode = pythonAllItems
	}
	task := map[string]interface{}{
		"code":     code,
		"mode":     mode,
		"items":    jsonMaps(input.Items),
		"nodes":    referencedOutputs(code, input.NodeOutputs),
		"workflow": map[string]interface{}{"id": input.WorkflowID},
		"runIndex": input.RunIndex,
	}
	// The resume URL is signed, so it is only built for code that reads it.
	if strings.Contains(code, "_execution") {
		task["execution"] = workflow.ExecutionData(input.ExecutionID)
	}
	stdin, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("code node: input items: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, "-c", pythonBootstrap)
	cmd.Env = workflow.RunEnvFor(ctx, nodemgr.UserEnv(os.Environ()), input.ExecutionID) // what a workflow runs is never the operator
	nodemgr.Isolate(cmd)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, workflow.ErrExecutionCancelled
	}
	if runCtx.Err() != nil {
		return nil, fmt.Errorf("code node: execution timeout (%ds)", int(timeout/time.Second))
	}

	var res struct {
		Items []map[string]interface{} `json:"items"`
		Error *struct {
			Message   string   `json:"message"`
			Traceback []string `json:"traceback"`
		} `json:"error"`
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	if jerr := json.Unmarshal(out, &res); jerr != nil || (res.Items == nil && res.Error == nil) {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = "…" + msg[len(msg)-2000:]
		}
		if runErr == nil {
			runErr = errors.New("no result")
		}
		return nil, fmt.Errorf("code node: Python (%s) failed: %v: %s", bin, runErr, msg)
	}
	if res.Error != nil {
		msg := "code node: Python error: " + res.Error.Message
		if len(res.Error.Traceback) > 0 {
			msg += "\n" + strings.Join(res.Error.Traceback, "\n")
		}
		return nil, errors.New(msg)
	}
	items := make([]workflow.Item, len(res.Items))
	for i, j := range res.Items {
		items[i] = workflow.Item{JSON: j}
	}
	if err := validateItems(items); err != nil {
		return nil, err
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

// referencedOutputs are the outputs of the nodes the code names, which
// _('Node Name') reads; the others never reach Python.
func referencedOutputs(code string, outputs map[string][]workflow.Item) map[string][]interface{} {
	m := map[string][]interface{}{}
	for name, items := range outputs {
		if name != "" && strings.Contains(code, name) {
			m[name] = jsonMaps(items)
		}
	}
	return m
}
