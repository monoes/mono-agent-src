package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/aimemory"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// DefaultSessionKey is the session key used when none is set: the sessionId
// field of the item, which is what n8n's chat trigger puts on its items.
const DefaultSessionKey = "{{ $json.sessionId }}"

// MemoryNode reads, inserts and deletes the messages of a chat session in
// the profile's conversation memory (internal/aimemory) — the same memory
// agent.ask's "memory" option reads and appends to.
//
// Config fields:
//
//	"operation" (string): get (default), insert or delete.
//	"session_key" (string): the session, resolved per item (default {{ $json.sessionId }}).
//	get:    "format" simple (default, [{role, content}]) | pairs ({human, ai, system}
//	        groups, n8n's simplified shape) | raw ({role, content, seq, created_at});
//	        "group" (bool, default true): one item {…input, messages, messagesCount}
//	        per input item, or one item per message.
//	insert: "messages" ([{role: human|ai|system, content}] or a JSON string of it;
//	        both fields resolved per item); "insert_mode" append (default) | override.
//	delete: "delete_mode" last_n (default) | all; "count" (default 2).
type MemoryNode struct{}

func (n *MemoryNode) Type() string { return "agent.memory" }

// PerItemConfigFields holds the session key and the messages back from the
// engine's one-pass resolution (first item only): each item resolves its own.
func (n *MemoryNode) PerItemConfigFields() []string { return []string{"session_key", "messages"} }

func (n *MemoryNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	op := configString(config, "operation", "get")
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("agent.memory: chat memory needs the profile database")
	}
	profile := vault.ProfileIDFromContext(ctx)
	sessionTmpl := configString(config, "session_key", DefaultSessionKey)

	var (
		messages           []map[string]interface{}
		format, insertMode string
		deleteMode         string
		count              int
		group              = true
	)
	switch op {
	case "get":
		format = configString(config, "format", "simple")
		if format != "simple" && format != "pairs" && format != "raw" {
			return nil, fmt.Errorf("%w: agent.memory: format %q (use simple, pairs or raw)", workflow.ErrInvalidConfig, format)
		}
		if g, ok := config["group"].(bool); ok {
			group = g
		}
	case "insert":
		insertMode = configString(config, "insert_mode", "append")
		if insertMode != "append" && insertMode != "override" {
			return nil, fmt.Errorf("%w: agent.memory: insert_mode %q (use append or override)", workflow.ErrInvalidConfig, insertMode)
		}
		var err error
		if messages, err = parseMessages(config["messages"]); err != nil {
			return nil, fmt.Errorf("%w: agent.memory: %v", workflow.ErrInvalidConfig, err)
		}
	case "delete":
		deleteMode = configString(config, "delete_mode", "last_n")
		if deleteMode != "last_n" && deleteMode != "all" {
			return nil, fmt.Errorf("%w: agent.memory: delete_mode %q (use last_n or all)", workflow.ErrInvalidConfig, deleteMode)
		}
		count = configInt(config, "count", 2)
	default:
		return nil, fmt.Errorf("%w: agent.memory: operation %q (use get, insert or delete)", workflow.ErrInvalidConfig, op)
	}

	in := input.Items
	if len(in) == 0 {
		in = []workflow.Item{{JSON: map[string]interface{}{}}}
	}
	var out []workflow.Item
	for i, item := range in {
		session, err := resolveTemplate(input, sessionTmpl, item)
		if err != nil {
			return nil, fmt.Errorf("agent.memory: session_key: %w", err)
		}
		if session = strings.TrimSpace(session); session == "" {
			return nil, fmt.Errorf("agent.memory: item %d has no session key (session_key %q resolved to nothing; the default reads the item's sessionId field)", i, sessionTmpl)
		}
		switch op {
		case "get":
			msgs, err := aimemory.Recent(ctx, db, profile, session, 0)
			if err != nil {
				return nil, fmt.Errorf("agent.memory: %w", err)
			}
			shaped := shapeMessages(msgs, format)
			if group {
				j := copyItemJSON(item)
				list := make([]interface{}, len(shaped))
				for k, m := range shaped {
					list[k] = m
				}
				j["messages"], j["messagesCount"] = list, len(shaped)
				out = append(out, workflow.Item{JSON: j})
			} else {
				for _, m := range shaped {
					out = append(out, workflow.Item{JSON: m})
				}
			}
		case "insert":
			batch := make([]aimemory.NewMessage, 0, len(messages))
			for k, m := range messages {
				role, err := resolveTemplate(input, fmt.Sprint(valueOr(m["role"], "human")), item)
				if err != nil {
					return nil, fmt.Errorf("agent.memory: messages[%d].role: %w", k, err)
				}
				content, err := resolveTemplate(input, fmt.Sprint(valueOr(m["content"], "")), item)
				if err != nil {
					return nil, fmt.Errorf("agent.memory: messages[%d].content: %w", k, err)
				}
				batch = append(batch, aimemory.NewMessage{Role: role, Content: content})
			}
			unlock := aimemory.LockSession(profile, session)
			if insertMode == "override" {
				err = aimemory.Replace(ctx, db, profile, session, batch)
			} else {
				err = aimemory.AppendMany(ctx, db, profile, session, batch)
			}
			unlock()
			if err != nil {
				return nil, fmt.Errorf("agent.memory: %w", err)
			}
			out = append(out, workflow.Item{JSON: map[string]interface{}{"success": true}})
		case "delete":
			unlock := aimemory.LockSession(profile, session)
			if deleteMode == "all" {
				_, err = aimemory.Clear(ctx, db, profile, session)
			} else {
				_, err = aimemory.Delete(ctx, db, profile, session, count)
			}
			unlock()
			if err != nil {
				return nil, fmt.Errorf("agent.memory: %w", err)
			}
			out = append(out, workflow.Item{JSON: map[string]interface{}{"success": true}})
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func valueOr(v interface{}, def string) interface{} {
	if v == nil {
		return def
	}
	return v
}

// parseMessages reads the insert operation's messages: a list of
// {role, content} objects, or a JSON string of one.
func parseMessages(v interface{}) ([]map[string]interface{}, error) {
	var list []interface{}
	switch x := v.(type) {
	case nil:
		return nil, fmt.Errorf("insert needs \"messages\": [{\"role\": \"human\", \"content\": \"…\"}]")
	case []interface{}:
		list = x
	case []map[string]interface{}:
		for _, m := range x {
			list = append(list, m)
		}
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, fmt.Errorf("insert needs \"messages\"")
		}
		if err := json.Unmarshal([]byte(x), &list); err != nil {
			return nil, fmt.Errorf("messages is not a JSON array: %w", err)
		}
	default:
		return nil, fmt.Errorf("messages must be a list of {role, content}, got %T", v)
	}
	out := make([]map[string]interface{}, 0, len(list))
	for i, e := range list {
		m, ok := e.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("messages[%d] is not an object", i)
		}
		out = append(out, m)
	}
	return out, nil
}

// shapeMessages renders stored messages in the get operation's format.
func shapeMessages(msgs []aimemory.Message, format string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(msgs))
	switch format {
	case "raw":
		for _, m := range msgs {
			out = append(out, map[string]interface{}{"role": m.Role, "content": m.Content, "seq": m.Seq, "created_at": m.CreatedAt})
		}
	case "pairs":
		// Consecutive messages share a group until a role repeats, so a
		// human message and its answer read as one {human, ai} entry.
		var cur map[string]interface{}
		for _, m := range msgs {
			if cur == nil || cur[m.Role] != nil {
				cur = map[string]interface{}{}
				out = append(out, cur)
			}
			cur[m.Role] = m.Content
		}
	default:
		for _, m := range msgs {
			out = append(out, map[string]interface{}{"role": m.Role, "content": m.Content})
		}
	}
	return out
}

// resolveTemplate evaluates a {{ }} template against one item, like any
// other config field.
func resolveTemplate(input workflow.NodeInput, tmpl string, item workflow.Item) (string, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	return workflow.NewExpressionEngine().EvaluateString(tmpl, workflow.ExpressionContext{
		JSON:        item.JSON,
		Node:        input.NodeOutputs,
		WorkflowID:  input.WorkflowID,
		ExecutionID: input.ExecutionID,
		Location:    input.Location,
	})
}
