package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/accountdoor"
)

func (s *Server) handleLine(ctx context.Context, line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		}
	}
	// The jsonrpc member is mandatory and must be exactly "2.0" — a
	// missing or wrong version is an Invalid Request, not a parse error.
	if req.JSONRPC != "2.0" {
		id := json.RawMessage("null")
		if hasRequestID(req) {
			id = req.ID
		}
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &rpcError{Code: -32600, Message: `invalid request: jsonrpc member must be "2.0"`},
		}
	}
	isNotificationMethod := strings.HasPrefix(req.Method, "notifications/")

	// A notifications/* method with a non-null id is a protocol violation.
	if isNotificationMethod && hasRequestID(req) {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32600, Message: "invalid request: " + req.Method + " is a notification and must not carry an id"},
		}
	}
	// Notifications (id-less requests) never get a response — for any
	// method, per JSON-RPC 2.0.
	if isNotificationMethod || !hasRequestID(req) {
		return nil
	}

	switch req.Method {
	case "initialize":
		s.recordClient(req.Params)
		capabilities := map[string]interface{}{
			"tools": map[string]interface{}{"listChanged": false},
		}
		if s.servesResources() {
			capabilities["resources"] = map[string]interface{}{"listChanged": false, "subscribe": false}
		}
		return s.result(req.ID, map[string]interface{}{
			"protocolVersion": negotiateProtocolVersion(req.Params),
			"capabilities":    capabilities,
			"serverInfo": map[string]interface{}{
				"name":    "monoagentcli",
				"version": s.version(),
			},
			"instructions": s.instructions(),
		})

	case "ping":
		return s.result(req.ID, map[string]interface{}{})

	case "tools/list":
		if s.opts.Grant != "" {
			// A grant's tools are the org's automations, read from the database: a locked
			// account lists none and opens nothing.
			if account.Require(ctx) != nil {
				return s.result(req.ID, map[string]interface{}{"tools": []map[string]interface{}{}})
			}
			return s.result(req.ID, map[string]interface{}{"tools": s.grantToolDefinitions(ctx)})
		}
		return s.result(req.ID, map[string]interface{}{"tools": definitionsOf(s.servedTools(), s.opts.AllowMutations)})

	case "tools/call":
		return s.handleToolsCall(ctx, req)

	case "resources/list", "resources/templates/list", "resources/read":
		if !s.servesResources() {
			break
		}
		return s.handleResources(ctx, req)
	}
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
	}
}

// handleToolsCall runs one tool. A locked monoes.me account refuses every call,
// grant mode included, with a tool error (isError) that says what to do, before
// the call is parsed. initialize and tools/list still answer.
func (s *Server) handleToolsCall(ctx context.Context, req rpcRequest) *rpcResponse {
	if err := account.Require(ctx); err != nil {
		return s.result(req.ID, toolResult{
			Content: []toolContent{{Type: "text", Text: accountdoor.Message}},
			IsError: true,
		})
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid tools/call params: " + err.Error()},
			}
		}
	}
	var result string
	var structured json.RawMessage
	var err error
	if s.opts.Grant != "" {
		result, err = s.callGrantTool(withMeta(ctx, params.Meta), params.Name, params.Arguments)
	} else {
		result, structured, err = callToolResult(withProgress(ctx, params.Meta), s, params.Name, params.Arguments)
	}
	text := result
	if err != nil {
		text, structured = err.Error(), nil
	}
	return s.result(req.ID, toolResult{
		Content:           []toolContent{{Type: "text", Text: text}},
		StructuredContent: structured,
		IsError:           err != nil,
	})
}

// closeRuntime releases the lazily-bootstrapped runtime, if any. (A plain
// `defer s.rt.Close()` would capture s.rt at defer time — before lazy
// bootstrap — and no-op.) Safe when the runtime was never built.
func (s *Server) closeRuntime() {
	s.refusals.wait() // an in-flight refusal row would otherwise rebuild the runtime just closed
	s.rtMu.Lock()
	rt := s.rt
	s.rt = nil // a later Serve builds a new runtime; a closed one has no database to resolve a profile with
	s.rtMu.Unlock()
	rt.Close()
}

func (s *Server) instructions() string {
	if s.opts.Grant != "" {
		return "Tools here run automations your org granted you. Each call starts a workflow run; outputs are redacted and bounded."
	}
	if s.opts.TasksOnly {
		return tasksOnlyInstructions
	}
	if s.opts.APIOnly {
		return "Tools here manage the OpenAI-compatible API: its keys, its models, its status and its settings. Start with api_status or api_config_get. The full rules of every tool are the resource monoagent://ref/api-tools."
	}
	return "Start with docs(topic) or workflow_list; validate before run; hil_list for pending approvals. The user's task board: task_next shows what is ready to work on."
}

func (s *Server) result(id json.RawMessage, result interface{}) *rpcResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) version() string {
	if s.opts.Version == "" {
		return "dev"
	}
	return s.opts.Version
}
