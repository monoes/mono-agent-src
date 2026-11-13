package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes/browserjev"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ── workflow tools ───────────────────────────────────────────────────────────

func toolWorkflowList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	wfs, err := rt.listWorkflows(ctx, profileOf(ctx))
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	// Node counts without an N+1: one aggregate query over workflow_nodes
	// covers SQL-stored workflows; file-store workflows already arrive
	// with their nodes parsed by ListWorkflows.
	nodeCounts := map[string]int{}
	rows, err := rt.db.DB.QueryContext(ctx,
		`SELECT workflow_id, COUNT(*) FROM workflow_nodes GROUP BY workflow_id`)
	if err != nil {
		return nil, fmt.Errorf("count workflow nodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan node count: %w", err)
		}
		nodeCounts[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node counts: %w", err)
	}

	out := []map[string]interface{}{}
	for _, wf := range wfs {
		nodeCount := len(wf.Nodes)
		if nodeCount == 0 {
			nodeCount = nodeCounts[wf.ID]
		}
		out = append(out, map[string]interface{}{
			"id":         wf.ID,
			"name":       wf.Name,
			"active":     wf.IsActive,
			"node_count": nodeCount,
		})
	}
	return out, nil
}

func toolWorkflowGet(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	wf, err := rt.store.GetWorkflow(ctx, a.ID)
	if err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}
	if wf == nil || !workflowOwned(wf, profileOf(ctx)) {
		return nil, fmt.Errorf("workflow %q not found", a.ID)
	}
	return wf, nil
}

// workflowOwned is workflow.OwnedBy for workflows: a workflow with no profile,
// or a call with none, is owned by nobody (read as "not found" by every
// caller here).
func workflowOwned(wf *workflow.Workflow, profileID string) bool {
	ok, _ := workflow.OwnedBy(wf, profileID)
	return ok
}

// crossProfile reports whether a non-workflow record's owning profile (empty
// means unscoped/legacy, and is considered in-profile) differs from the
// active profile. Workflows use workflowOwned. A cross-profile lookup is surfaced as "not found" by
// every caller here, matching internal/httpapi/handlers.go's
// handleWorkflowGet/handleWorkflowExecutions and
// internal/workflow/engine.go's checkWorkflowProfile — never a
// distinguishable "forbidden", so a guessed or leaked ID from another
// profile can't be used to probe for existence.
func crossProfile(ownerProfileID, activeProfileID string) bool {
	return ownerProfileID != "" && ownerProfileID != activeProfileID
}

// listWorkflows is the workflows of one profile. The hybrid store lists every
// workflow kept as a file whatever profile it belongs to, so those of other
// profiles are dropped here, by the same rule every by-id lookup applies.
func (rt *runtime) listWorkflows(ctx context.Context, profileID string) ([]workflow.Workflow, error) {
	all, err := rt.store.ListWorkflows(ctx, profileID)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, wf := range all {
		if workflowOwned(&wf, profileID) {
			out = append(out, wf)
		}
	}
	return out, nil
}

// parseWorkflowBytes accepts both the file-store workflow JSON format
// (type/source/target keys) and the legacy DB export format, mirroring
// `monoagentcli workflow import`.
func parseWorkflowBytes(raw []byte) (workflow.Workflow, error) {
	wf, err := workflow.ParseWorkflowFileBytes(raw)
	if err == nil {
		return wf, nil
	}
	var legacy workflow.Workflow
	if err2 := json.Unmarshal(raw, &legacy); err2 != nil {
		return wf, err
	}
	return legacy, nil
}

func toolWorkflowValidate(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID       string                 `json:"id"`
		File     string                 `json:"file"`
		Workflow map[string]interface{} `json:"workflow"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}

	var wf workflow.Workflow
	switch {
	case a.ID != "":
		rt, err := s.runtime()
		if err != nil {
			return nil, err
		}
		loaded, err := rt.store.GetWorkflow(ctx, a.ID)
		if err != nil {
			return nil, fmt.Errorf("get workflow: %w", err)
		}
		if loaded == nil || !workflowOwned(loaded, profileOf(ctx)) {
			return nil, fmt.Errorf("workflow %q not found", a.ID)
		}
		wf = *loaded
	case a.File != "":
		raw, err := os.ReadFile(a.File)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("file not found: %s", a.File)
			}
			// Collapse non-existence read errors (permissions, dirs, …)
			// to a fixed message: no path-derived or OS-error detail.
			return nil, fmt.Errorf("file is not a valid workflow")
		}
		wf, err = parseWorkflowBytes(raw)
		if err != nil {
			// No content-derived parse detail either.
			return nil, fmt.Errorf("file is not a valid workflow")
		}
	case a.Workflow != nil:
		raw, err := json.Marshal(a.Workflow)
		if err != nil {
			return nil, fmt.Errorf("render workflow JSON: %w", err)
		}
		if wf, err = parseWorkflowBytes(raw); err != nil {
			return nil, fmt.Errorf("parse workflow JSON: %w", err)
		}
	default:
		return nil, fmt.Errorf("provide one of: id, file, or workflow")
	}

	// ValidateWorkflow runs ValidateForActivation (structure, DAG,
	// triggers) plus the schema-aware checks, as `workflow validate` does.
	rep := workflow.ValidateWorkflow(&wf, workflow.ValidateOptions{KnownNodeType: noderegistry.Build(nil).Has})
	return map[string]interface{}{
		"valid":    rep.Valid,
		"errors":   rep.Errors,
		"warnings": rep.Warnings,
		"issues":   rep.Issues,
	}, nil
}

func toolWorkflowRun(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID             string                 `json:"id"`
		Input          map[string]interface{} `json:"input"`
		TimeoutSeconds float64                `json:"timeout_seconds"`
		// WaitThroughSubworkflows keeps waiting while the run is paused only
		// for its own sub-workflows (this server's engine runs them and
		// resumes it); by default any WAITING ends the wait.
		WaitThroughSubworkflows bool `json:"wait_through_subworkflows"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	// Another profile's workflow is not found, as for workflow_get: the engine of this
	// profile would refuse it too, but with a message that says the workflow exists.
	if owned, gerr := rt.store.GetWorkflow(ctx, a.ID); gerr == nil && owned != nil && !workflowOwned(owned, profileOf(ctx)) {
		return nil, fmt.Errorf("workflow %q not found", a.ID)
	}
	engine, err := rt.ensureEngine(ctx, profileOf(ctx))
	if err != nil {
		return nil, err
	}

	workflow.StripEngineKeys(a.Input)
	// The caller of this tool is an agent, not the user's terminal: its run carries an agent's mark, as
	// `workflow run` gives an agent's run, so a node that refuses an AI run refuses this one.
	if a.Input == nil {
		a.Input = map[string]interface{}{}
	}
	browserjev.MarkAICallerFor(ctx, nil, a.Input, "an MCP caller is not the user's terminal", false)
	execID, err := engine.TriggerWorkflow(ctx, a.ID, a.Input)
	if err != nil {
		return nil, fmt.Errorf("trigger workflow: %w", err)
	}

	timeout := clampTimeoutSeconds(a.TimeoutSeconds)
	var exec *workflow.WorkflowExecution
	if p := progressFrom(ctx); p != nil {
		exec = waitWithProgress(ctx, rt, execID, a.ID, timeout, a.WaitThroughSubworkflows, p)
	} else {
		exec = waitForExecution(ctx, rt, execID, timeout, a.WaitThroughSubworkflows)
	}
	// An agent reaches this server: a reply's token in the run's data is
	// hidden from it (workflow.ReplyRedactor), as in workflow_status.
	workflow.NewReplyRedactor(ctx, rt.db.DB, exec.ID).Execution(exec)

	nodeTypes := map[string]string{}
	if wf, err := rt.store.GetWorkflow(ctx, exec.WorkflowID); err == nil && wf != nil {
		for _, n := range wf.Nodes {
			nodeTypes[n.ID] = n.Type
		}
	}

	nodes := []map[string]interface{}{}
	for _, en := range exec.Nodes {
		outputItems := workflow.RedactAndTruncateItems(en.OutputItems)
		if outputItems == nil {
			outputItems = []workflow.Item{}
		}
		row := map[string]interface{}{
			"node_id":      en.NodeID,
			"type":         nodeTypes[en.NodeID],
			"status":       en.Status,
			"output_items": outputItems,
		}
		if en.ErrorMessage != "" {
			row["error"] = en.ErrorMessage
		}
		nodes = append(nodes, row)
	}
	result := map[string]interface{}{
		"execution_id": exec.ID,
		"status":       exec.Status,
		"nodes":        nodes,
	}
	if exec.ErrorMessage != "" {
		result["error"] = exec.ErrorMessage
	}
	timedOut := exec.Status == "RUNNING" || exec.Status == "QUEUED"
	if a.WaitThroughSubworkflows {
		// Still paused for its sub-workflows when the wait ran out: not
		// finished either.
		timedOut, _ = workflow.RecordInFlight(ctx, rt.store, exec)
	}
	if timedOut {
		result["timed_out"] = true
	}
	return result, nil
}

// waitForExecution polls the execution's STATUS only (a single-column
// query) until it reaches a terminal or paused status, the timeout
// elapses, or ctx is cancelled; the full record (with node outputs) is
// then loaded exactly once. Returns the latest snapshot either way. With
// throughSubs a pause only for the run's own sub-workflows is waited
// through (runInFlight).
func waitForExecution(ctx context.Context, rt *runtime, executionID string, timeout time.Duration, throughSubs bool) *workflow.WorkflowExecution {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	load := func() *workflow.WorkflowExecution {
		exec, err := rt.store.GetExecution(context.Background(), executionID)
		if err != nil || exec == nil {
			return &workflow.WorkflowExecution{ID: executionID, Status: "UNKNOWN"}
		}
		return exec
	}
	for {
		select {
		case <-ticker.C:
			status, inFlight, err := runInFlight(ctx, rt, executionID, throughSubs)
			if err != nil || status == "" {
				continue
			}
			if !inFlight || time.Now().After(deadline) {
				return load() // final, or paused (WAITING)
			}
		case <-ctx.Done():
			return load()
		}
	}
}

func toolWorkflowStatus(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.ExecutionID == "" {
		return nil, fmt.Errorf("execution_id is required")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	exec, err := rt.store.GetExecution(ctx, a.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("get execution: %w", err)
	}
	if exec == nil || crossProfile(exec.ProfileID, profileOf(ctx)) {
		return nil, fmt.Errorf("execution %q not found", a.ExecutionID)
	}
	// Same output pipeline as workflow_run: credential-key redaction (no
	// opt-out over MCP) followed by the per-item size cap, and reply tokens
	// hidden.
	workflow.NewReplyRedactor(ctx, rt.db.DB, exec.ID).Execution(exec)
	for i := range exec.Nodes {
		exec.Nodes[i].OutputItems = workflow.RedactAndTruncateItems(exec.Nodes[i].OutputItems)
	}
	if exec.Status != "WAITING" {
		return exec, nil
	}
	// A run paused for a reply (core.wait webhook/form, approval links) is
	// resumed only through its URL: name it.
	waiting, err := workflow.PendingReplies(ctx, rt.db.DB, exec.ID)
	if err != nil {
		return nil, err
	}
	// An agent reaches this server: a reply that lets a message to a person
	// go out is that person's or the user's to answer, so its links stay out.
	waiting = workflow.RedactReplies(waiting)
	return struct {
		*workflow.WorkflowExecution
		Waiting []workflow.PendingReply `json:"waiting_for,omitempty"`
	}{exec, waiting}, nil
}

// runInFlight is whether a run's wait goes on: it is QUEUED or RUNNING, or,
// with throughSubs, WAITING only for its own sub-workflows, which this
// server's engine runs and then resumes it (workflow.ExecutionInFlight).
func runInFlight(ctx context.Context, rt *runtime, executionID string, throughSubs bool) (string, bool, error) {
	if throughSubs {
		return workflow.ExecutionInFlight(ctx, rt.store, executionID)
	}
	status, err := rt.store.GetExecutionStatus(ctx, executionID)
	return status, status == "QUEUED" || status == "RUNNING", err
}

// clampTimeoutSeconds maps the workflow_run timeout_seconds argument to a
// duration clamped to [1s, 600s]; zero/negative values fall back to the
// 120s default. The clamp bounds how long one tool call can run.
func clampTimeoutSeconds(secs float64) time.Duration {
	switch {
	case secs <= 0:
		return 120 * time.Second
	case secs < 1:
		return 1 * time.Second
	case secs > 600:
		return 600 * time.Second
	default:
		return time.Duration(secs * float64(time.Second))
	}
}
