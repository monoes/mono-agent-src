// Package aisub provides the AI sub-nodes (n8n's cluster sub-nodes): a chat
// model, a conversation memory and a structured output parser. They never
// run as steps; a root node (agent.ask, ai.guardrails, agent.memory) reads
// the ones attached to it through "ai_…" connections from
// workflow.NodeInput.SubNodes.
package aisub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Sub-node types and the connection kinds they attach as.
const (
	TypeModel        = "ai.model"
	TypeMemory       = "ai.memory"
	TypeOutputParser = "ai.output_parser"

	KindModel        = "ai_languageModel"
	KindMemory       = "ai_memory"
	KindOutputParser = "ai_outputParser"
)

// defaultSessionKey matches agent.DefaultSessionKey.
const defaultSessionKey = "{{ $json.sessionId }}"

// subNode is the executor of a sub-node type: it only fails, since the
// engine never runs an attached sub-node as a step.
type subNode struct{ typ, kind string }

func (n *subNode) Type() string { return n.typ }

func (n *subNode) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	return nil, fmt.Errorf("%w: %s is a sub-node: connect its %s output to Agent Ask", workflow.ErrInvalidConfig, n.typ, n.kind)
}

// PerItemConfigFields leaves a memory's session key unresolved, so the root
// resolves it against each of its items.
func (n *subNode) PerItemConfigFields() []string {
	if n.typ == TypeMemory {
		return []string{"session_key"}
	}
	return nil
}

// RegisterAll registers the sub-node types.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	for typ, kind := range map[string]string{TypeModel: KindModel, TypeMemory: KindMemory, TypeOutputParser: KindOutputParser} {
		typ, kind := typ, kind
		r.Register(typ, func() workflow.NodeExecutor { return &subNode{typ: typ, kind: kind} })
	}
}

// first returns the first attached sub-node of kind (a second one of a
// one-only kind is ignored).
func first(in workflow.NodeInput, kind string) (workflow.SubNode, bool) {
	subs := in.SubNodesOf(kind)
	if len(subs) == 0 {
		return workflow.SubNode{}, false
	}
	return subs[0], true
}

// Model is an attached chat model.
type Model struct {
	Name, Runtime, Model string
}

// ModelOf returns the chat model attached to the node, if any.
func ModelOf(in workflow.NodeInput) (Model, bool) {
	s, ok := first(in, KindModel)
	if !ok {
		return Model{}, false
	}
	name := s.Name
	if name == "" {
		name = s.ID
	}
	return Model{Name: name, Runtime: str(s.Config, "runtime"), Model: str(s.Config, "model")}, true
}

// MemoryOf returns the attached memory as agent.ask's memory option
// {session_key, window, skip_when_missing}.
func MemoryOf(in workflow.NodeInput) (map[string]interface{}, bool) {
	s, ok := first(in, KindMemory)
	if !ok {
		return nil, false
	}
	key := str(s.Config, "session_key")
	if strings.TrimSpace(key) == "" {
		key = defaultSessionKey
	}
	m := map[string]interface{}{"session_key": key}
	if w, ok := s.Config["window"]; ok && w != nil && w != "" {
		m["window"] = w
	}
	if v, ok := s.Config["skip_when_missing"]; ok && v != nil {
		m["skip_when_missing"] = v
	}
	return m, true
}

// OutputSchemaOf returns the JSON Schema of the attached output parser.
func OutputSchemaOf(in workflow.NodeInput) (map[string]interface{}, bool, error) {
	s, ok := first(in, KindOutputParser)
	if !ok {
		return nil, false, nil
	}
	schema, err := BuildSchema(s.Config)
	if err != nil {
		return nil, true, fmt.Errorf("output parser %q: %w", s.Name, err)
	}
	return schema, true, nil
}

// BuildSchema builds an output parser's JSON Schema from its config:
// schema_type example (json_example, every key required) or schema
// (json_schema as written).
func BuildSchema(cfg map[string]interface{}) (map[string]interface{}, error) {
	switch t := str(cfg, "schema_type"); t {
	case "", "example":
		v, err := jsonValue(cfg["json_example"])
		if err != nil {
			return nil, fmt.Errorf("json_example: %w", err)
		}
		if v == nil {
			return nil, fmt.Errorf("json_example is empty: give an example of the JSON to answer with")
		}
		return SchemaFromExample(v), nil
	case "schema":
		v, err := jsonValue(cfg["json_schema"])
		if err != nil {
			return nil, fmt.Errorf("json_schema: %w", err)
		}
		m, ok := v.(map[string]interface{})
		if !ok || len(m) == 0 {
			return nil, fmt.Errorf("json_schema must be a JSON Schema object")
		}
		return m, nil
	default:
		return nil, fmt.Errorf("schema_type %q: use example or schema", t)
	}
}

// jsonValue reads a JSON value given as a string or already decoded; an
// empty string is nil.
func jsonValue(v interface{}) (interface{}, error) {
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out interface{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	return out, nil
}

// SchemaFromExample builds a JSON Schema from an example value, as n8n's
// "Generate From JSON Example" does: every key of the example is required.
func SchemaFromExample(v interface{}) map[string]interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		props, required := map[string]interface{}{}, []interface{}{}
		for _, k := range keys {
			props[k] = SchemaFromExample(x[k])
			required = append(required, k)
		}
		return map[string]interface{}{"type": "object", "properties": props, "required": required}
	case []interface{}:
		s := map[string]interface{}{"type": "array"}
		if len(x) > 0 {
			s["items"] = SchemaFromExample(x[0])
		}
		return s
	case float64, int:
		return map[string]interface{}{"type": "number"}
	case bool:
		return map[string]interface{}{"type": "boolean"}
	case nil:
		return map[string]interface{}{"type": "null"}
	}
	return map[string]interface{}{"type": "string"}
}

func str(m map[string]interface{}, key string) string {
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		return fmt.Sprint(v)
	}
}
