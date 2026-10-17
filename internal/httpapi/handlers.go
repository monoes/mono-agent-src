package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ── workflows ─────────────────────────────────────────────────────────────

// workflowInProfile is workflow.OwnedBy, the one ownership rule: a workflow
// with no profile (or a request with none) is owned by nobody.
func workflowInProfile(wf *workflow.Workflow, profileID string) bool {
	ok, _ := workflow.OwnedBy(wf, profileID)
	return ok
}

// Load through the request's profile boundary even when the host's engine
// intentionally serves all profiles (the daemon). A workflow with no profile
// belongs to nobody (workflow.OwnedBy).
func (s *Server) loadWorkflow(w http.ResponseWriter, r *http.Request) *workflow.Workflow {
	id := r.PathValue("id")
	wf, err := s.rt.store.GetWorkflow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get workflow: "+err.Error())
		return nil
	}
	if !workflowInProfile(wf, profileOf(r)) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("workflow %q not found", id))
		return nil
	}
	return wf
}

// GET /profiles/{profile}/workflows
func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wfs, err := s.rt.store.ListWorkflows(ctx, profileOf(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list workflows: "+err.Error())
		return
	}

	nodeCounts := map[string]int{}
	rows, err := s.rt.db.DB.QueryContext(ctx, `SELECT workflow_id, COUNT(*) FROM workflow_nodes GROUP BY workflow_id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count workflow nodes: "+err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			writeError(w, http.StatusInternalServerError, "scan node count: "+err.Error())
			return
		}
		nodeCounts[id] = n
	}

	out := []map[string]interface{}{}
	for _, wf := range wfs {
		if !workflowInProfile(&wf, profileOf(r)) {
			continue
		}
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
	writeJSON(w, http.StatusOK, out)
}

// GET /profiles/{profile}/workflows/{id}
func (s *Server) handleWorkflowGet(w http.ResponseWriter, r *http.Request) {
	wf := s.loadWorkflow(w, r)
	if wf == nil {
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

// GET /profiles/{profile}/workflows/{id}/executions
func (s *Server) handleWorkflowExecutions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if s.loadWorkflow(w, r) == nil {
		return
	}

	limit := 20
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}

	execs, err := s.rt.store.ListExecutions(ctx, id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list executions: "+err.Error())
		return
	}
	ids := make([]string, len(execs))
	for i := range execs {
		execs[i].Nodes = nil // list view: summary only, no per-node output items
		ids[i] = execs[i].ID
	}
	rr := workflow.NewReplyRedactor(ctx, s.rt.db.DB, ids...)
	for i := range execs {
		rr.Execution(&execs[i])
	}
	writeJSON(w, http.StatusOK, execs)
}

// parseWorkflowBytes accepts both the file-store workflow JSON format
// (type/source/target keys) and the legacy DB export format, mirroring
// `monoagentcli workflow import` and internal/mcp's workflow_validate tool.
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

// POST /profiles/{profile}/workflows/{id}/validate
//
// Validates the saved workflow named by {id}. A body is optional: when
// present and non-empty, it is treated as an inline workflow JSON document
// to validate INSTEAD of the saved one (id is still required by the route
// but only used to shape the URL — this mirrors the MCP workflow_validate
// tool's id/workflow modes without a second route).
func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request) {

	var wf workflow.Workflow
	body, err := readLimitedBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		wf, err = parseWorkflowBytes(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "request body is not a valid workflow: "+err.Error())
			return
		}
	} else {
		loaded := s.loadWorkflow(w, r)
		if loaded == nil {
			return
		}
		wf = *loaded
	}

	errs := []string{}
	if err := workflow.ValidateForSave(&wf); err != nil {
		errs = append(errs, err.Error())
	} else if err := workflow.ValidateForActivation(&wf); err != nil {
		errs = append(errs, err.Error())
	}
	for _, is := range workflow.NodeConfigIssues(wf.Nodes) {
		errs = append(errs, fmt.Sprintf("node %q: %s", is.NodeID, is.Message))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":  len(errs) == 0,
		"errors": errs,
	})
}

// POST /profiles/{profile}/workflows/{id}/run  (mutating)
func (s *Server) handleWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if s.loadWorkflow(w, r) == nil {
		return
	}

	var body struct {
		Input          map[string]interface{} `json:"input"`
		TimeoutSeconds float64                `json:"timeout_seconds"`
	}
	raw, err := readLimitedBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}

	engine, err := s.rt.engineFor(ctx, profileOf(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	workflow.StripEngineKeys(body.Input)
	execID, err := engine.TriggerWorkflow(ctx, id, body.Input)
	if err != nil {
		writeErrorFor(w, err)
		return
	}

	timeout := clampTimeoutSeconds(body.TimeoutSeconds)
	exec := waitForExecution(ctx, s.rt, execID, timeout)
	// A key holder can be an agent, as over MCP: reply tokens in the run's
	// data are hidden (workflow.ReplyRedactor), X-Full-Outputs or not.
	workflow.NewReplyRedactor(ctx, s.rt.db.DB, exec.ID).Execution(exec)

	nodeTypes := map[string]string{}
	if wf, err := s.rt.store.GetWorkflow(ctx, exec.WorkflowID); err == nil && wf != nil {
		for _, n := range wf.Nodes {
			nodeTypes[n.ID] = n.Type
		}
	}

	nodesOut := []map[string]interface{}{}
	for _, en := range exec.Nodes {
		items := redactItemsUnlessFull(r, en.OutputItems)
		if items == nil {
			items = []workflow.Item{}
		}
		row := map[string]interface{}{
			"node_id":      en.NodeID,
			"type":         nodeTypes[en.NodeID],
			"status":       en.Status,
			"output_items": items,
		}
		if en.ErrorMessage != "" {
			row["error"] = en.ErrorMessage
		}
		nodesOut = append(nodesOut, row)
	}
	result := map[string]interface{}{
		"execution_id": exec.ID,
		"status":       exec.Status,
		"nodes":        nodesOut,
	}
	if exec.ErrorMessage != "" {
		result["error"] = exec.ErrorMessage
	}
	if exec.Status == "RUNNING" || exec.Status == "QUEUED" {
		result["timed_out"] = true
	}
	writeJSON(w, http.StatusOK, result)
}

// waitForExecution polls the execution's status until it reaches a
// terminal or paused status, the timeout elapses, or ctx is cancelled.
// Mirrors internal/mcp/tools.go's waitForExecution.
func waitForExecution(ctx context.Context, rt *runtime, executionID string, timeout time.Duration) *workflow.WorkflowExecution {
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
			status, err := rt.store.GetExecutionStatus(ctx, executionID)
			if err != nil || status == "" {
				continue
			}
			switch status {
			case "QUEUED", "RUNNING":
				if time.Now().After(deadline) {
					return load()
				}
			default: // SUCCESS, FAILED, CANCELLED, WAITING (HIL pause)
				return load()
			}
		case <-ctx.Done():
			return load()
		}
	}
}

// clampTimeoutSeconds maps the run timeout_seconds body field to a
// duration clamped to [1s, 600s]; zero/negative falls back to 120s.
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

// POST /profiles/{profile}/workflows/{id}/activate  (mutating)
func (s *Server) handleWorkflowActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if s.loadWorkflow(w, r) == nil {
		return
	}
	engine, err := s.rt.engineFor(ctx, profileOf(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := engine.ActivateWorkflow(ctx, id); err != nil {
		writeErrorFor(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":     id,
		"status": "activated",
		"note":   "triggers are only served for as long as this server process stays running (or `monoagentcli daemon` if that's separately running) — see AGENTS.md",
	})
}

// POST /profiles/{profile}/workflows/{id}/deactivate  (mutating)
func (s *Server) handleWorkflowDeactivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if s.loadWorkflow(w, r) == nil {
		return
	}
	engine, err := s.rt.engineFor(ctx, profileOf(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := engine.DeactivateWorkflow(ctx, id); err != nil {
		writeErrorFor(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "status": "deactivated"})
}

// ── body helpers ─────────────────────────────────────────────────────────

// maxBodyBytes bounds request bodies (validate/run/hil-approve payloads)
// to keep one request from exhausting memory.
const maxBodyBytes = 4 * 1024 * 1024

func readLimitedBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxBodyBytes)
	}
	return body, nil
}
