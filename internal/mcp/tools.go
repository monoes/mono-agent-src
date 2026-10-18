package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// outputSchema, when set, is the JSON schema of the tool's structuredContent.
	outputSchema map[string]interface{}
	mutating     bool // gated behind Options.AllowMutations — see callTool/toolDefinitions
	// global marks a tool that reads and writes no profile data (node types,
	// docs, bundled workflow templates, machine-wide message templates and API
	// settings). Every other tool is profile-scoped: scoped() adds the required
	// `profile` argument to its schema and callToolResult resolves it per call.
	global  bool
	handler toolHandler
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
	return definitionsOf(allTools(), allowMutations)
}

// definitionsOf is toolDefinitions over the tools a server serves (see servedTools).
func definitionsOf(tools []tool, allowMutations bool) []map[string]interface{} {
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
		if t.outputSchema != nil {
			def["outputSchema"] = t.outputSchema
		}
		out = append(out, def)
	}
	return out
}

// callTool dispatches a tools/call invocation and renders the result as
// JSON text. Errors are returned as Go errors; the server renders them as
// tool results with isError=true (per MCP spec), never protocol errors.
func callTool(ctx context.Context, s *Server, name string, args json.RawMessage) (string, error) {
	text, _, err := callToolResult(ctx, s, name, args)
	return text, err
}

// callToolResult is callTool plus the result as structuredContent: the
// compact JSON of a result that is a JSON object, nil for any other result
// (an array, a string, a fenced text).
func callToolResult(ctx context.Context, s *Server, name string, args json.RawMessage) (string, json.RawMessage, error) {
	for _, t := range s.servedTools() {
		if t.name != name {
			continue
		}
		if t.mutating && !s.opts.AllowMutations {
			return "", nil, fmt.Errorf("%s requires --allow-mutations (or MONOAGENT_MCP_ALLOW_MUTATIONS=1) on the mcp server", t.name)
		}
		if !t.global {
			var perr error
			if ctx, args, perr = s.scopeCall(ctx, args); perr != nil {
				return "", nil, perr
			}
		}
		result, err := t.handler(ctx, s, args)
		if err != nil {
			return "", nil, err
		}
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", nil, fmt.Errorf("render tool result: %w", err)
		}
		return string(b), structuredOf(b), nil
	}
	if s.opts.APIOnly || s.opts.TasksOnly {
		for _, t := range allTools() {
			if t.name == name {
				return "", nil, s.notServed(name) // it exists, and this server does not serve it
			}
		}
	}
	return "", nil, fmt.Errorf("unknown tool %q", name)
}

// structuredOf is rendered as compact JSON when it is a JSON object, else nil.
func structuredOf(rendered []byte) json.RawMessage {
	trimmed := bytes.TrimSpace(rendered)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var buf bytes.Buffer
	if json.Compact(&buf, trimmed) != nil {
		return nil
	}
	return json.RawMessage(buf.Bytes())
}

func allTools() []tool {
	native := []tool{
		{
			name:        "workflow_list",
			description: "List saved workflows for the call's profile: [{id, name, active, node_count}]",
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
			annotations:  map[string]bool{"readOnlyHint": true},
			outputSchema: validateOutputSchema,
			handler:      toolWorkflowValidate,
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
			annotations:  map[string]bool{"readOnlyHint": false},
			mutating:     true,
			outputSchema: runOutputSchema,
			handler:      toolWorkflowRun,
		},
		{
			name:        "workflow_status",
			description: "Get an execution record by ID, including per-node output items",
			schema: objSchema(map[string]interface{}{
				"execution_id": strParam("Execution ID from workflow_run"),
			}, "execution_id"),
			annotations:  map[string]bool{"readOnlyHint": true},
			outputSchema: statusOutputSchema,
			handler:      toolWorkflowStatus,
		},
		{
			name:        "node_list",
			global:      true, // reads no profile data
			description: "List available node types: [{type, category, title}]. Optionally filter by category — one of: trigger, control, http, system, communication, ai, browser/social, people, database, service, data, other. category is a derived grouping, not a type-string prefix (e.g. category=\"browser/social\" returns instagram.*/linkedin.*/gemini.*/etc together; a raw type name like \"gemini\" matches nothing).",
			schema: objSchema(map[string]interface{}{
				"category": strParam("Optional category filter — see the tool description for valid values"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolNodeList,
		},
		{
			name:        "node_schema",
			global:      true, // reads no profile data
			description: "Get the embedded JSON schema (config fields) for one node type",
			schema: objSchema(map[string]interface{}{
				"type": strParam("Node type, e.g. http.request or trigger.schedule"),
			}, "type"),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolNodeSchema,
		},
		{
			name:        "hil_list",
			description: "List pending Human-in-Loop approval items for the call's profile",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolHilList,
		},
		{
			name:        "hil_approve",
			description: "Approve a pending Human-in-Loop item, optionally overriding its editable data with a JSON object. Refused for a browser form submit, a site an agent asked to use and a send: the user decides those in their own terminal or on the Human in Loop page",
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
			description: "Reject a pending Human-in-Loop item (the paused workflow branch fails). Refused for a browser form submit, a site an agent asked to use and a send: the user decides those",
			schema: objSchema(map[string]interface{}{
				"id": strParam("HIL item ID"),
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": true},
			mutating:    true,
			handler:     toolHilReject,
		},
		{
			name:        "docs",
			global:      true, // reads no profile data
			description: "Built-in reference docs (same facts as the `monoagentcli ref` command). Topics: commands, nodes, expressions, workflow, templates, connections, org, publication, api-tools. Omit topic for the index. Also served as resources monoagent://ref/<topic>.",
			schema: objSchema(map[string]interface{}{
				"topic": strParam("Topic name (optional)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolDocs,
		},
	}
	native = append(native, monoagentAdaptedTools()...)
	native = append(native, authoringTools()...)
	native = append(native, nodeRunTool(), doctorTool())
	native = append(native, taskTools()...)
	native = append(native, apiTools()...)
	native = append(native, publicationTools()...)
	native = append(native, generateTools()...)
	return scoped(append(native, apiConfigTools()...))
}

// The outputSchemas of the core workflow tools: what their structuredContent holds.
var (
	validateOutputSchema = objSchema(map[string]interface{}{
		"valid":  map[string]interface{}{"type": "boolean"},
		"errors": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
	}, "valid", "errors")
	runOutputSchema = objSchema(map[string]interface{}{
		"execution_id": map[string]interface{}{"type": "string"},
		"status":       map[string]interface{}{"type": "string", "description": "SUCCESS, FAILED, CANCELLED, WAITING (a Human-in-Loop pause), RUNNING or QUEUED (timed out), UNKNOWN"},
		"nodes": map[string]interface{}{"type": "array", "items": objSchema(map[string]interface{}{
			"node_id":      map[string]interface{}{"type": "string"},
			"type":         map[string]interface{}{"type": "string"},
			"status":       map[string]interface{}{"type": "string"},
			"output_items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object"}},
			"error":        map[string]interface{}{"type": "string"},
		}, "node_id", "status", "output_items")},
		"error":     map[string]interface{}{"type": "string"},
		"timed_out": map[string]interface{}{"type": "boolean"},
	}, "execution_id", "status", "nodes")
	statusOutputSchema = objSchema(map[string]interface{}{
		"id":            map[string]interface{}{"type": "string"},
		"workflow_id":   map[string]interface{}{"type": "string"},
		"profile_id":    map[string]interface{}{"type": "string"},
		"status":        map[string]interface{}{"type": "string"},
		"trigger_type":  map[string]interface{}{"type": "string"},
		"trigger_data":  map[string]interface{}{"type": []string{"object", "null"}},
		"started_at":    map[string]interface{}{"type": []string{"string", "null"}},
		"finished_at":   map[string]interface{}{"type": []string{"string", "null"}},
		"error_message": map[string]interface{}{"type": "string"},
		"created_at":    map[string]interface{}{"type": "string"},
		"nodes":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object"}},
		"waiting_for":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object"}},
	}, "id", "workflow_id", "status")
)

func decodeArgs(args json.RawMessage, dst interface{}) error {
	if len(args) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
