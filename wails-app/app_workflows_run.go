package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// RunWorkflow spawns `monoagentcli workflow run <id>` as a subprocess with no
// trigger data. Kept as its own binding so existing callers (the Dashboard's
// run button) are unchanged.
func (a *App) RunWorkflow(id string) error {
	return a.RunWorkflowWithInput(id, "")
}

// RunWorkflowWithInput is RunWorkflow with trigger data — the GUI equivalent
// of `workflow run <id> --input '{"prompts":[…]}'`.
//
// Without it a manual-trigger workflow whose nodes read {{ $json.<field> }}
// was simply unrunnable from the GUI: the run button sent nothing, every such
// expression resolved to nothing, and the run did nothing. inputJSON must be
// a JSON object; empty means no trigger data.
func (a *App) RunWorkflowWithInput(id, inputJSON string) error {
	trimmed := strings.TrimSpace(inputJSON)
	if trimmed != "" {
		var probe map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
			return fmt.Errorf("trigger input must be a JSON object: %w", err)
		}
	}
	return a.runWorkflowProcess(id, trimmed)
}

func (a *App) runWorkflowProcess(id, inputJSON string) error {
	// `workflow get` is profile-scoped, so another profile's id is not found.
	wf, err := a.GetWorkflow(id)
	if err != nil {
		return err
	}
	// The engine rejects inactive workflows — surface that here instead of
	// silently flipping is_active behind the user's back.
	if !wf.IsActive {
		return fmt.Errorf("workflow %q is inactive — activate it first", wf.Name)
	}
	// Refuse a second concurrent run of the same workflow: runningCmds is
	// keyed by workflow ID, so starting another would orphan the first
	// subprocess (unkillable via CancelWorkflow).
	a.runningMu.Lock()
	if _, running := a.runningCmds[id]; running {
		a.runningMu.Unlock()
		return fmt.Errorf("workflow %s is already running", id)
	}
	a.runningMu.Unlock()

	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}

	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Starting workflow %s", id))

	runArgs := []string{"--profile", a.getActiveProfileID(), "workflow", "run", id}
	if inputJSON != "" {
		runArgs = append(runArgs, "--input", inputJSON)
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, cliBin, runArgs...)
	suppressConsole(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	// Reserve the registry slot before Start so a concurrent RunWorkflow
	// call loses the race cleanly; released on Start failure or process exit.
	a.runningMu.Lock()
	a.runningCmds[id] = cmd
	a.runningMu.Unlock()

	if err := cmd.Start(); err != nil {
		a.runningMu.Lock()
		delete(a.runningCmds, id)
		a.runningMu.Unlock()
		return fmt.Errorf("failed to start workflow: %w", err)
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Workflow %s started (pid %d)", id, cmd.Process.Pid))

	var execID string // set once the CLI names the execution
	notSetup := &agentSetupWatch{}
	var outputDone sync.WaitGroup
	outputDone.Add(2)
	go func() {
		defer outputDone.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			a.emitLog("WORKFLOW", "INFO", line)
			notSetup.note(line)
			// Detect execution ID from CLI output and notify the frontend
			if strings.HasPrefix(line, "Execution started: ") {
				started := strings.TrimSpace(strings.TrimPrefix(line, "Execution started: "))
				// Also track the process by execution id, so CancelWorkflow
				// can kill it without looking the execution up.
				a.runningMu.Lock()
				if a.runningCmds[id] == cmd {
					execID = started
					a.runningCmds[execID] = cmd
				}
				a.runningMu.Unlock()
				a.emitWorkflowEvent("workflow:exec-started", map[string]interface{}{
					"workflow_id":  id,
					"execution_id": started,
				})
			}
		}
	}()
	go func() {
		defer outputDone.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.emitLog("WORKFLOW", "INFO", scanner.Text())
			notSetup.note(scanner.Text())
		}
	}()
	go func() {
		waitErr := cmd.Wait()
		// Wait closed the pipes, so the readers end; notSetup is final.
		outputDone.Wait()
		a.runningMu.Lock()
		for key, c := range a.runningCmds {
			if c == cmd {
				delete(a.runningCmds, key)
			}
		}
		a.runningMu.Unlock()
		if waitErr != nil {
			a.emitLog("WORKFLOW", "ERROR", fmt.Sprintf("Workflow %s failed: %v", id, waitErr))
			a.emitWorkflowEvent("workflow:complete", notSetup.apply(map[string]interface{}{"workflow_id": id, "success": false}))
		} else {
			a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Workflow %s completed", id))
			a.emitWorkflowEvent("workflow:complete", map[string]interface{}{"workflow_id": id, "success": true})
		}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "images:changed", map[string]interface{}{"profileID": a.getActiveProfileID(), "workflow_id": id})
		}
	}()
	return nil
}

// emitWorkflowEvent emits to the frontend once the Wails runtime is up
// (emitLog's guard): tests drive the run bindings without one.
// agentSetupWatch keeps the run output line that says the run failed
// because the AI agent is not set up (the CLI marks it with
// agentNotSetupMarker), so the failed workflow:complete carries
// code agent_not_setup and the frontend can link to the AI agents page.
type agentSetupWatch struct {
	mu   sync.Mutex
	line string
}

func (w *agentSetupWatch) note(line string) {
	if strings.Contains(line, agentNotSetupMarker) {
		w.mu.Lock()
		w.line = strings.TrimSpace(line)
		w.mu.Unlock()
	}
}

// apply adds "code" and "error" to a failed run's event when a marked line
// was seen.
func (w *agentSetupWatch) apply(ev map[string]interface{}) map[string]interface{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.line != "" {
		ev["code"], ev["error"] = agentNotSetupCode, w.line
	}
	return ev
}

func (a *App) emitWorkflowEvent(name string, data map[string]interface{}) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, name, data)
	}
}

// workflowPollCLITimeout bounds the execution bindings the editor polls
// while a run is in progress.
const workflowPollCLITimeout = 20 * time.Second

// GetWorkflowExecutions returns `workflow executions <id> --limit <n>`,
// newest first (50 when limit is not positive).
func (a *App) GetWorkflowExecutions(workflowID string, limit int) ([]WorkflowExecutionSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []workflow.WorkflowExecution
	if err := a.cliJSON(workflowPollCLITimeout, &rows, "workflow", "executions", workflowID, "--limit", strconv.Itoa(limit)); err != nil {
		return nil, err
	}
	stamp := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format(time.RFC3339Nano)
	}
	execs := make([]WorkflowExecutionSummary, 0, len(rows))
	for _, e := range rows {
		execs = append(execs, WorkflowExecutionSummary{
			ID:          e.ID,
			WorkflowID:  e.WorkflowID,
			Status:      e.Status,
			TriggerType: e.TriggerType,
			StartedAt:   stamp(e.StartedAt),
			FinishedAt:  stamp(e.FinishedAt),
			Error:       e.ErrorMessage,
			CreatedAt:   stamp(&e.CreatedAt),
		})
	}
	return execs, nil
}

// GetExecutionDetail returns `workflow execution <id>`: the execution with
// per-node status, items already redacted by the CLI. The editor reads each
// node's input_items/output_items as a JSON string, as it always has.
func (a *App) GetExecutionDetail(executionID string) (map[string]interface{}, error) {
	var d struct {
		ID          string `json:"id"`
		WorkflowID  string `json:"workflow_id"`
		Status      string `json:"status"`
		TriggerType string `json:"trigger_type"`
		StartedAt   string `json:"started_at"`
		FinishedAt  string `json:"finished_at"`
		Error       string `json:"error_message"`
		CreatedAt   string `json:"created_at"`
		Nodes       []struct {
			ID           string          `json:"id"`
			NodeID       string          `json:"node_id"`
			NodeName     string          `json:"node_name"`
			Status       string          `json:"status"`
			ErrorMessage string          `json:"error_message"`
			StartedAt    string          `json:"started_at"`
			FinishedAt   string          `json:"finished_at"`
			InputItems   json.RawMessage `json:"input_items"`
			OutputItems  json.RawMessage `json:"output_items"`
			RetryCount   int             `json:"retry_count"`
		} `json:"nodes"`
	}
	if err := a.cliJSON(workflowPollCLITimeout, &d, "workflow", "execution", executionID); err != nil {
		return nil, err
	}
	nodesList := make([]map[string]interface{}, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		nodesList = append(nodesList, map[string]interface{}{
			"id":            n.ID,
			"node_id":       n.NodeID,
			"node_name":     n.NodeName,
			"status":        n.Status,
			"error_message": n.ErrorMessage,
			"started_at":    n.StartedAt,
			"finished_at":   n.FinishedAt,
			"input_items":   itemsString(n.InputItems),
			"output_items":  itemsString(n.OutputItems),
			"retry_count":   n.RetryCount,
		})
	}
	return map[string]interface{}{
		"id":           d.ID,
		"workflow_id":  d.WorkflowID,
		"status":       d.Status,
		"trigger_type": d.TriggerType,
		"started_at":   d.StartedAt,
		"finished_at":  d.FinishedAt,
		"error":        d.Error,
		"created_at":   d.CreatedAt,
		"nodes":        nodesList,
	}, nil
}

// itemsString turns the CLI's item array back into the compact JSON string
// the editor parses; a value the CLI kept as a string comes back unchanged.
func itemsString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

// CancelWorkflow cancels a running workflow execution. A process this app
// started for it (RunWorkflow) is the app's to kill; the rest — stopping a
// process started elsewhere, which must never be the daemon, and marking
// the execution cancelled — is `workflow cancel <id>`.
func (a *App) CancelWorkflow(executionID string) error {
	a.runningMu.Lock()
	if cmd, ok := a.runningCmds[executionID]; ok {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		for key, c := range a.runningCmds {
			if c == cmd {
				delete(a.runningCmds, key)
			}
		}
	}
	a.runningMu.Unlock()

	if err := a.runMonoCLI("", nil, "workflow", "cancel", executionID); err != nil {
		a.emitLog("WORKFLOW", "ERROR", fmt.Sprintf("Execution %s: %v", executionID, err))
		return err
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Execution %s cancelled", executionID))
	return nil
}
