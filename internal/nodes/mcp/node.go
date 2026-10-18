// Package mcpnode provides the mcp.call workflow node: an MCP client that
// lists or calls the tools of any MCP server, spawned over stdio or reached
// over Streamable HTTP.
package mcpnode

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// NodeType is the registered node type.
const NodeType = "mcp.call"

const defaultTimeoutSecs = 60

// perItemFields are resolved against each item's own JSON.
var perItemFields = []string{"tool", "arguments"}

// secretRefPattern finds @secret:<name> references inside a header or env
// value, e.g. "Bearer @secret:zapier".
var secretRefPattern = regexp.MustCompile(`@secret:([^\s"']+)`)

// resolveSecret turns "@secret:<name>" into the vault value. A var so tests
// can stand in for the vault.
var resolveSecret = func(ctx context.Context, ref string) (string, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return "", fmt.Errorf("no vault available to resolve %s", ref)
	}
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return "", fmt.Errorf("mcp.call ran without a profile: %w", perr)
	}
	return secrets.Resolve(ctx, db, profile, ref)
}

// CallNode is the mcp.call node.
//
// Config fields:
//
//	"server" (string): name of a vault secret holding the server as JSON
//	  {transport, command, args, env, cwd, url, headers}; node fields override it.
//	"transport" (string): stdio | http (default: http when url is set).
//	"command", "args" (list), "env" (map), "cwd": the stdio server to spawn.
//	"url", "headers" (map): the Streamable HTTP endpoint.
//	  env and header values may hold @secret:<name> references.
//	"operation" (string): list_tools | call_tool (default call_tool when tool is set).
//	"tool" (string), "arguments" (object): the tool call, resolved per item.
//	"timeout" (number): seconds per MCP request (default 60).
//	"fail_on_tool_error" (bool): a result with isError fails the node (default true).
type CallNode struct{}

func (n *CallNode) Type() string { return NodeType }

// PerItemConfigFields holds tool and arguments back from batch-wide
// resolution so each item's call reads its own $json.
func (n *CallNode) PerItemConfigFields() []string { return perItemFields }

// RegisterAll registers mcp.call.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register(NodeType, func() workflow.NodeExecutor { return &CallNode{} })
}

// serverConfig describes how to reach one MCP server.
type serverConfig struct {
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Cwd       string            `json:"cwd"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

func invalid(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s: %s", workflow.ErrInvalidConfig, NodeType, fmt.Sprintf(format, a...))
}

func (n *CallNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	srv, err := loadServer(ctx, config)
	if err != nil {
		return nil, err
	}
	operation := str(config, "operation")
	if operation == "" {
		operation = "call_tool"
		if str(config, "tool") == "" {
			operation = "list_tools"
		}
	}
	if operation != "list_tools" && operation != "call_tool" {
		return nil, invalid("operation must be list_tools or call_tool, got %q", operation)
	}
	if operation == "call_tool" && str(config, "tool") == "" {
		return nil, invalid(`call_tool needs "tool"`)
	}
	timeout := time.Duration(defaultTimeoutSecs) * time.Second
	if v, ok := num(config["timeout"]); ok && v > 0 {
		timeout = time.Duration(v * float64(time.Second))
	}
	failOnToolError := true
	if b, ok := config["fail_on_tool_error"].(bool); ok {
		failOnToolError = b
	}

	// What a workflow runs is never the operator (workflow.EnvWorkflowRun).
	if srv.Env == nil {
		srv.Env = map[string]string{}
	}
	srv.Env[workflow.EnvWorkflowRun] = workflow.RunMarker(input.ExecutionID)
	if srv.Transport != "http" {
		if err := workflow.RefuseUnapprovedProgram(ctx, NodeType+" (a stdio server)", input.WorkflowID); err != nil {
			return nil, err
		}
	}
	t, err := connect(srv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", NodeType, err)
	}
	defer t.close()
	c := &client{t: t, timeout: timeout}
	if err := c.initialize(ctx); err != nil {
		return nil, fmt.Errorf("%s: %w", NodeType, err)
	}

	var items []workflow.Item
	if operation == "list_tools" {
		tools, err := c.listTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", NodeType, err)
		}
		for _, tool := range tools {
			items = append(items, workflow.NewItem(toolItem(tool)))
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
	}

	inputs := input.Items
	if len(inputs) == 0 {
		inputs = []workflow.Item{workflow.NewItem(map[string]interface{}{})}
	}
	engine := workflow.NewExpressionEngine()
	for i, item := range inputs {
		name, args, err := resolveCall(engine, config, item, input)
		if err != nil {
			return nil, err
		}
		res, err := c.callTool(ctx, name, args)
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", NodeType, i, err)
		}
		if res.IsError && failOnToolError {
			return nil, fmt.Errorf("%s: tool %q returned an error: %s", NodeType, name, errorText(res))
		}
		items = append(items, workflow.NewItem(resultItem(name, res)))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

func connect(srv *serverConfig) (transport, error) {
	if srv.Transport == "http" {
		return newHTTPTransport(srv.URL, srv.Headers), nil
	}
	return startStdio(srv.Command, srv.Args, srv.Env, srv.Cwd)
}

// loadServer builds the server config from the optional vault entry and
// the node's own fields, and resolves @secret: refs in env and headers.
func loadServer(ctx context.Context, config map[string]interface{}) (*serverConfig, error) {
	srv := &serverConfig{}
	if name := str(config, "server"); name != "" {
		raw := name
		// The engine already resolved a "@secret:" value; a bare name is
		// looked up here.
		if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
			v, err := resolveSecret(ctx, "@secret:"+strings.TrimPrefix(name, "@secret:"))
			if err != nil {
				return nil, invalid("server %q: %v", name, err)
			}
			raw = v
		}
		if err := json.Unmarshal([]byte(raw), srv); err != nil {
			return nil, invalid("server secret %q is not a JSON server definition: %v", name, err)
		}
	}
	if s := str(config, "transport"); s != "" {
		srv.Transport = s
	}
	if s := str(config, "command"); s != "" {
		srv.Command = s
	}
	if args, err := stringList(config["args"]); err != nil {
		return nil, invalid("args: %v", err)
	} else if args != nil {
		srv.Args = args
	}
	if s := str(config, "cwd"); s != "" {
		srv.Cwd = s
	}
	if s := str(config, "url"); s != "" {
		srv.URL = s
	}
	for key, dst := range map[string]*map[string]string{"env": &srv.Env, "headers": &srv.Headers} {
		m, err := stringMap(config[key])
		if err != nil {
			return nil, invalid("%s: %v", key, err)
		}
		if len(m) > 0 && *dst == nil {
			*dst = map[string]string{}
		}
		for k, v := range m {
			(*dst)[k] = v
		}
	}
	if srv.Transport == "" {
		srv.Transport = "stdio"
		if srv.URL != "" && srv.Command == "" {
			srv.Transport = "http"
		}
	}
	switch srv.Transport {
	case "stdio":
		if srv.Command == "" {
			return nil, invalid(`stdio transport needs "command"`)
		}
	case "http":
		if !strings.HasPrefix(srv.URL, "http://") && !strings.HasPrefix(srv.URL, "https://") {
			return nil, invalid(`http transport needs an http(s) "url", got %q`, srv.URL)
		}
	default:
		return nil, invalid("transport must be stdio or http, got %q", srv.Transport)
	}
	for _, m := range []map[string]string{srv.Env, srv.Headers} {
		for k, v := range m {
			r, err := expandSecrets(ctx, v)
			if err != nil {
				return nil, invalid("%s: %v", k, err)
			}
			m[k] = r
		}
	}
	return srv, nil
}

// expandSecrets replaces every @secret:<name> in s with its vault value.
// An unresolvable reference is an error: the literal is never sent.
func expandSecrets(ctx context.Context, s string) (string, error) {
	var firstErr error
	out := secretRefPattern.ReplaceAllStringFunc(s, func(ref string) string {
		v, err := resolveSecret(ctx, ref)
		if err == nil && strings.HasPrefix(v, "@secret:") {
			err = fmt.Errorf("%s did not resolve", ref)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return ref
		}
		return v
	})
	return out, firstErr
}

// resolveCall evaluates tool and arguments against one item.
func resolveCall(engine *workflow.ExpressionEngine, config map[string]interface{}, item workflow.Item, input workflow.NodeInput) (string, map[string]interface{}, error) {
	overlay := map[string]interface{}{}
	for _, k := range perItemFields {
		if v, ok := config[k]; ok {
			overlay[k] = v
		}
	}
	resolved, err := engine.ResolveConfig(overlay, workflow.ExpressionContext{
		JSON:        item.JSON,
		Node:        input.NodeOutputs,
		WorkflowID:  input.WorkflowID,
		ExecutionID: input.ExecutionID,
	})
	if err != nil {
		return "", nil, fmt.Errorf("%s: resolve per-item config: %w", NodeType, err)
	}
	name := str(resolved, "tool")
	if name == "" {
		return "", nil, invalid(`"tool" resolved to an empty name`)
	}
	var args map[string]interface{}
	switch a := resolved["arguments"].(type) {
	case nil:
	case map[string]interface{}:
		args = a
	case string:
		if strings.TrimSpace(a) != "" {
			if err := json.Unmarshal([]byte(a), &args); err != nil {
				return "", nil, invalid("arguments must be a JSON object: %v", err)
			}
		}
	default:
		return "", nil, invalid("arguments must be an object, got %T", a)
	}
	return name, args, nil
}

func toolItem(tool map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{"tool": tool["name"]}
	for src, dst := range map[string]string{
		"title": "title", "description": "description", "inputSchema": "input_schema",
		"outputSchema": "output_schema", "annotations": "annotations",
	} {
		if v, ok := tool[src]; ok {
			out[dst] = v
		}
	}
	return out
}

func resultItem(name string, res *toolResult) map[string]interface{} {
	content := make([]interface{}, len(res.Content))
	for i, c := range res.Content {
		content[i] = c
	}
	result := res.StructuredContent
	if result == nil {
		if text, ok := joinedText(res); ok {
			var parsed interface{}
			if json.Unmarshal([]byte(text), &parsed) == nil {
				result = parsed
			} else {
				result = text
			}
		}
	}
	return map[string]interface{}{
		"tool":     name,
		"result":   result,
		"content":  content,
		"is_error": res.IsError,
	}
}

// joinedText joins the result's text content blocks.
func joinedText(res *toolResult) (string, bool) {
	var parts []string
	for _, c := range res.Content {
		if c["type"] == "text" {
			if s, ok := c["text"].(string); ok {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

func errorText(res *toolResult) string {
	s, ok := joinedText(res)
	if !ok {
		return "(no text content)"
	}
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return s
}

func str(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func num(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// stringList accepts a list, a JSON array string, or one argument per line.
func stringList(v interface{}) ([]string, error) {
	switch a := v.(type) {
	case nil:
		return nil, nil
	case []interface{}:
		out := make([]string, len(a))
		for i, e := range a {
			out[i] = fmt.Sprint(e)
		}
		return out, nil
	case []string:
		return a, nil
	case string:
		s := strings.TrimSpace(a)
		if s == "" {
			return nil, nil
		}
		if strings.HasPrefix(s, "[") {
			var out []string
			if err := json.Unmarshal([]byte(s), &out); err != nil {
				return nil, fmt.Errorf("not a JSON array of strings: %v", err)
			}
			return out, nil
		}
		var out []string
		for _, line := range strings.Split(s, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("want a list, got %T", v)
}

// stringMap accepts an object or a JSON object string.
func stringMap(v interface{}) (map[string]string, error) {
	var m map[string]interface{}
	switch a := v.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		m = a
	case map[string]string:
		return a, nil
	case string:
		if strings.TrimSpace(a) == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(a), &m); err != nil {
			return nil, fmt.Errorf("not a JSON object: %v", err)
		}
	default:
		return nil, fmt.Errorf("want an object, got %T", v)
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = fmt.Sprint(val)
	}
	return out, nil
}

// LiteralConfigFields: server names the vault entry the server is read from
// (workflow.LiteralConfigResolver).
func (n *CallNode) LiteralConfigFields() []string { return []string{"server"} }

// SecretTextFields: env and header values may hold @secret references inside
// their text (workflow.SecretTextResolver).
func (n *CallNode) SecretTextFields() []string { return []string{"env", "headers"} }
