package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

type toolHandler func(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error)

// tool is one MCP tool descriptor. annotations carries the MCP tool
// annotations (readOnlyHint, destructiveHint, idempotentHint) so hosts can
// surface safety hints; nil means no annotations are emitted.
type tool struct {
	name        string
	description string
	schema      map[string]interface{}
	annotations map[string]bool
	mutating    bool // gated behind Options.AllowMutations — see callTool/toolDefinitions
	handler     toolHandler
}

func strParam(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func objSchema(props map[string]interface{}, required ...string) map[string]interface{} {
	if props == nil {
		props = map[string]interface{}{}
	}
	m := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// toolDefinitions returns the MCP tool descriptors for tools/list. Mutating
// tools are omitted entirely when allowMutations is false, so a model never
// sees or attempts one it can't use — unlike internal/httpapi's equivalent
// gate, callTool below still answers a direct call to a gated tool by name
// with an explanatory refusal rather than "unknown tool": httpapi's
// omit-so-it-404s design specifically defeats a *remote* prober enumerating
// endpoints, a threat model that doesn't apply to a local stdio process the
// user configured themselves — and a clear refusal is more useful than a
// misleading "doesn't exist" for someone whose config used to work.
func toolDefinitions(allowMutations bool) []map[string]interface{} {
	tools := allTools()
	out := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		if t.mutating && !allowMutations {
			continue
		}
		def := map[string]interface{}{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.schema,
		}
		if t.annotations != nil {
			def["annotations"] = t.annotations
		}
		out = append(out, def)
	}
	return out
}

// callTool dispatches a tools/call invocation and renders the result as
// JSON text. Errors are returned as Go errors; the server renders them as
// tool results with isError=true (per MCP spec), never protocol errors.
func callTool(ctx context.Context, s *Server, name string, args json.RawMessage) (string, error) {
	for _, t := range allTools() {
		if t.name != name {
			continue
		}
		if t.mutating && !s.opts.AllowMutations {
			return "", fmt.Errorf("%s requires --allow-mutations (or MONOAGENT_MCP_ALLOW_MUTATIONS=1) on the mcp server", t.name)
		}
		result, err := t.handler(ctx, s, args)
		if err != nil {
			return "", err
		}
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("render tool result: %w", err)
		}
		return string(b), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func allTools() []tool {
	native := []tool{
		{
			name:        "workflow_list",
			description: "List saved workflows for the active profile: [{id, name, active, node_count}]",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolWorkflowList,
		},
		{
			name:        "workflow_get",
			description: "Get one saved workflow (full JSON definition: nodes + connections)",
			schema: objSchema(map[string]interface{}{
				"id": strParam("Workflow ID"),
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     toolWorkflowGet,
		},
		{
			name:        "workflow_validate",
			description: "Validate a workflow (structure + activation rules + DAG/cycle check). Returns {valid, errors[]}. Provide exactly one of: id (saved workflow), file (path to workflow JSON), or workflow (inline workflow JSON object).",
			schema: objSchema(map[string]interface{}{
				"id":       strParam("Saved workflow ID"),
				"file":     strParam("Path to a workflow JSON file"),
				"workflow": map[string]interface{}{"type": "object", "description": "Inline workflow JSON object"},
			}),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     toolWorkflowValidate,
		},
		{
			name:        "workflow_run",
			description: "Trigger a saved workflow and wait (default 120s). Returns {execution_id, status, nodes:[{node_id, type, status, output_items}]}. A WAITING status means a Human-in-Loop node paused the run — use hil_list / hil_approve.",
			schema: objSchema(map[string]interface{}{
				"id": strParam("Workflow ID"),
				"input": map[string]interface{}{
					"type":        "object",
					"description": "Trigger data object, available downstream as {{ $json.<field> }}",
				},
				"timeout_seconds": map[string]interface{}{"type": "number", "description": "Wait limit in seconds (default 120, clamped to [1, 600])"},
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     toolWorkflowRun,
		},
		{
			name:        "workflow_status",
			description: "Get an execution record by ID, including per-node output items",
			schema: objSchema(map[string]interface{}{
				"execution_id": strParam("Execution ID from workflow_run"),
			}, "execution_id"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     toolWorkflowStatus,
		},
		{
			name:        "node_list",
			description: "List available node types: [{type, category, title}]. Optionally filter by category — one of: trigger, control, http, system, communication, ai, browser/social, people, database, service, data, other. category is a derived grouping, not a type-string prefix (e.g. category=\"browser/social\" returns instagram.*/linkedin.*/gemini.*/etc together; a raw type name like \"gemini\" matches nothing).",
			schema: objSchema(map[string]interface{}{
				"category": strParam("Optional category filter — see the tool description for valid values"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolNodeList,
		},
		{
			name:        "node_schema",
			description: "Get the embedded JSON schema (config fields) for one node type",
			schema: objSchema(map[string]interface{}{
				"type": strParam("Node type, e.g. http.request or trigger.schedule"),
			}, "type"),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolNodeSchema,
		},
		{
			name:        "hil_list",
			description: "List pending Human-in-Loop approval items for the active profile",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolHilList,
		},
		{
			name:        "hil_approve",
			description: "Approve a pending Human-in-Loop item, optionally overriding its editable data with a JSON object",
			schema: objSchema(map[string]interface{}{
				"id": strParam("HIL item ID"),
				"data": map[string]interface{}{
					"type":        "object",
					"description": "JSON object overriding the item's editable data (default {})",
				},
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     toolHilApprove,
		},
		{
			name:        "hil_reject",
			description: "Reject a pending Human-in-Loop item (the paused workflow branch fails)",
			schema: objSchema(map[string]interface{}{
				"id": strParam("HIL item ID"),
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": true},
			mutating:    true,
			handler:     toolHilReject,
		},
		{
			name:        "docs",
			description: "Built-in reference docs (same facts as the `monoagentcli ref` command). Topics: commands, nodes, expressions, workflow, templates, connections, org, publication. Omit topic for the index.",
			schema: objSchema(map[string]interface{}{
				"topic": strParam("Topic name (optional)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolDocs,
		},
	}
	return append(append(native, monoagentAdaptedTools()...), publicationTools()...)
}

func decodeArgs(args json.RawMessage, dst interface{}) error {
	if len(args) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// ── workflow tools ───────────────────────────────────────────────────────────

func toolWorkflowList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	wfs, err := rt.store.ListWorkflows(ctx, rt.profileID)
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
	if wf == nil || crossProfile(wf.ProfileID, rt.profileID) {
		return nil, fmt.Errorf("workflow %q not found", a.ID)
	}
	return wf, nil
}

// crossProfile reports whether a record's owning profile (empty means
// unscoped/legacy, and is always considered in-profile) differs from the
// active profile. A cross-profile lookup is surfaced as "not found" by
// every caller here, matching internal/httpapi/handlers.go's
// handleWorkflowGet/handleWorkflowExecutions and
// internal/workflow/engine.go's checkWorkflowProfile — never a
// distinguishable "forbidden", so a guessed or leaked ID from another
// profile can't be used to probe for existence.
func crossProfile(ownerProfileID, activeProfileID string) bool {
	return ownerProfileID != "" && ownerProfileID != activeProfileID
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
		if loaded == nil || crossProfile(loaded.ProfileID, rt.profileID) {
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

	errs := []string{}
	// ValidateForSave covers structure + unique IDs + connection refs + the
	// DAG cycle check; ValidateForActivation adds trigger requirements.
	if err := workflow.ValidateForSave(&wf); err != nil {
		errs = append(errs, err.Error())
	} else if err := workflow.ValidateForActivation(&wf); err != nil {
		errs = append(errs, err.Error())
	}
	return map[string]interface{}{
		"valid":  len(errs) == 0,
		"errors": errs,
	}, nil
}

func toolWorkflowRun(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID             string                 `json:"id"`
		Input          map[string]interface{} `json:"input"`
		TimeoutSeconds float64                `json:"timeout_seconds"`
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
	if err := rt.ensureEngine(ctx); err != nil {
		return nil, err
	}

	execID, err := rt.engine.TriggerWorkflow(ctx, a.ID, a.Input)
	if err != nil {
		return nil, fmt.Errorf("trigger workflow: %w", err)
	}

	timeout := clampTimeoutSeconds(a.TimeoutSeconds)
	exec := waitForExecution(ctx, rt, execID, timeout)

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
	if exec.Status == "RUNNING" || exec.Status == "QUEUED" {
		result["timed_out"] = true
	}
	return result, nil
}

// waitForExecution polls the execution's STATUS only (a single-column
// query) until it reaches a terminal or paused status, the timeout
// elapses, or ctx is cancelled; the full record (with node outputs) is
// then loaded exactly once. Returns the latest snapshot either way.
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
	if exec == nil || crossProfile(exec.ProfileID, rt.profileID) {
		return nil, fmt.Errorf("execution %q not found", a.ExecutionID)
	}
	// Same output pipeline as workflow_run: credential-key redaction (no
	// opt-out over MCP) followed by the per-item size cap.
	for i := range exec.Nodes {
		exec.Nodes[i].OutputItems = workflow.RedactAndTruncateItems(exec.Nodes[i].OutputItems)
	}
	return exec, nil
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

// ── node tools ───────────────────────────────────────────────────────────────

// nodeCategory mirrors the CLI's node list categorization.
func nodeCategory(t string) string {
	legacy := map[string]string{
		"mysql": "database", "postgres": "database", "mongodb": "database", "redis": "database",
		"github": "service", "notion": "service", "airtable": "service", "jira": "service",
		"linear": "service", "asana": "service", "stripe": "service", "shopify": "service",
		"salesforce": "service", "hubspot": "service", "google_sheets": "service",
		"gmail": "service", "google_drive": "service",
		"datetime": "data", "crypto": "data", "html": "data", "xml": "data",
		"markdown": "data", "spreadsheet": "data", "compression": "data", "write_binary_file": "data",
	}
	switch {
	case strings.HasPrefix(t, "trigger."):
		return "trigger"
	case strings.HasPrefix(t, "core."), strings.HasPrefix(t, "control."):
		return "control"
	case strings.HasPrefix(t, "http."):
		return "http"
	case strings.HasPrefix(t, "system."):
		return "system"
	case strings.HasPrefix(t, "comm."):
		return "communication"
	case strings.HasPrefix(t, "ai."), strings.HasPrefix(t, "agent."), strings.HasPrefix(t, "browser."):
		return "ai"
	case strings.HasPrefix(t, "instagram."), strings.HasPrefix(t, "linkedin."),
		strings.HasPrefix(t, "x."), strings.HasPrefix(t, "tiktok."),
		strings.HasPrefix(t, "hackernews."), strings.HasPrefix(t, "producthunt."),
		strings.HasPrefix(t, "gemini."):
		return "browser/social"
	case strings.HasPrefix(t, "people."):
		return "people"
	case strings.HasPrefix(t, "db."), strings.HasPrefix(t, "mysql."), strings.HasPrefix(t, "postgres."):
		return "database"
	case strings.HasPrefix(t, "service."):
		return "service"
	case strings.HasPrefix(t, "data."):
		return "data"
	case legacy[t] != "":
		return legacy[t]
	default:
		return "other"
	}
}

// nodeTitle derives a human-friendly title from a node type string
// ("service.google_sheets" -> "Google Sheets").
func nodeTitle(t string) string {
	name := t
	if i := strings.Index(t, "."); i >= 0 {
		name = t[i+1:]
	}
	words := strings.Split(name, "_")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func toolNodeList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Category string `json:"category"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	types := rt.registryOrDefault().Types()
	sort.Strings(types)
	out := []map[string]interface{}{}
	for _, t := range types {
		cat := nodeCategory(t)
		if a.Category != "" && cat != a.Category {
			continue
		}
		out = append(out, map[string]interface{}{
			"type":     t,
			"category": cat,
			"title":    nodeTitle(t),
		})
	}
	return out, nil
}

func toolNodeSchema(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Type string `json:"type"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Type == "" {
		return nil, fmt.Errorf("type is required")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	// Trigger types (trigger.schedule, trigger.webhook, ...) have embedded
	// schemas but no executor in the registry; accept either source.
	registry := rt.registryOrDefault()
	hasSchema := false
	for _, name := range workflow.ListEmbeddedSchemas() {
		if name == a.Type {
			hasSchema = true
			break
		}
	}
	if !registry.Has(a.Type) && !hasSchema {
		return nil, fmt.Errorf("unknown node type %q — use node_list to see all types", a.Type)
	}
	schema, err := workflow.LoadDefaultSchema(a.Type)
	if err != nil {
		return nil, fmt.Errorf("load schema: %w", err)
	}
	return map[string]interface{}{
		"type":   a.Type,
		"schema": schema,
	}, nil
}

// ── Human-in-Loop tools ──────────────────────────────────────────────────────

type hilItem struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	NodeID       string                 `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Status       string                 `json:"status"`
	ReadonlyData map[string]interface{} `json:"readonly_data"`
	EditableData map[string]interface{} `json:"editable_data"`
	CreatedAt    string                 `json:"created_at"`
}

func toolHilList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rows, err := rt.db.DB.QueryContext(ctx,
		`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
		        h.readonly_data, h.editable_data, h.created_at, COALESCE(w.name,'')
		 FROM hil_pending h
		 LEFT JOIN workflows w ON w.id = h.workflow_id
		 WHERE h.status = 'pending' AND h.profile_id = ?
		 ORDER BY h.created_at ASC`,
		rt.profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("query HIL items: %w", err)
	}
	defer rows.Close()

	items := []hilItem{}
	for rows.Next() {
		var it hilItem
		var roRaw, edRaw string
		if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
			&it.Status, &roRaw, &edRaw, &it.CreatedAt, &it.WorkflowName); err != nil {
			return nil, fmt.Errorf("scan HIL item: %w", err)
		}
		_ = json.Unmarshal([]byte(roRaw), &it.ReadonlyData)
		_ = json.Unmarshal([]byte(edRaw), &it.EditableData)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate HIL items: %w", err)
	}
	return items, nil
}

func toolHilApprove(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID   string                 `json:"id"`
		Data map[string]interface{} `json:"data"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if a.Data == nil {
		a.Data = map[string]interface{}{}
	}
	edited, err := json.Marshal(a.Data)
	if err != nil {
		return nil, fmt.Errorf("render data: %w", err)
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	res, err := rt.db.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='pending' AND profile_id = ?`,
		string(edited), a.ID, rt.profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("approve HIL item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("HIL item %q not found or already resolved", a.ID)
	}
	return map[string]string{"id": a.ID, "status": "approved"}, nil
}

func toolHilReject(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
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
	res, err := rt.db.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='pending' AND profile_id = ?`,
		a.ID, rt.profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("reject HIL item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("HIL item %q not found or already resolved", a.ID)
	}
	return map[string]string{"id": a.ID, "status": "rejected"}, nil
}

// ── docs tool ────────────────────────────────────────────────────────────────

func toolDocs(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Topic string `json:"topic"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Topic == "" {
		topics := make([]string, 0, len(docsTopics))
		for t := range docsTopics {
			topics = append(topics, t)
		}
		sort.Strings(topics)
		return map[string]interface{}{
			"topics": topics,
			"hint":   "pass a topic to the docs tool for its content; `monoagentcli ref <topic>` covers the same ground in the CLI",
		}, nil
	}
	content, ok := docsTopics[a.Topic]
	if !ok {
		return nil, fmt.Errorf("unknown topic %q", a.Topic)
	}
	return map[string]string{"topic": a.Topic, "content": content}, nil
}
