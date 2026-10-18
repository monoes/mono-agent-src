package dbnodes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// resolveForItem resolves the held-back per-item fields of base against one
// item's JSON and returns a copy of base with the resolved values overlaid.
// It mirrors the pipeline RunExecution applies to the rest of the config:
// expression templates, then @img-NNN vault refs, then @secret:<name> refs.
// base is never mutated — it is shared by every item of the batch.
func resolveForItem(ctx context.Context, base map[string]interface{}, fields []string, item workflow.Item, input workflow.NodeInput) (map[string]interface{}, error) {
	overlay := make(map[string]interface{}, len(fields))
	for _, k := range fields {
		if v, ok := base[k]; ok {
			overlay[k] = v
		}
	}
	out := make(map[string]interface{}, len(base))
	for k, v := range base {
		out[k] = v
	}
	if len(overlay) == 0 {
		return out, nil
	}
	exprCtx := workflow.ExpressionContext{
		JSON:        item.JSON,
		Node:        input.NodeOutputs,
		WorkflowID:  input.WorkflowID,
		ExecutionID: input.ExecutionID,
	}
	if exprCtx.JSON == nil {
		exprCtx.JSON = map[string]interface{}{}
	}
	resolved, err := workflow.NewExpressionEngine().ResolveConfig(overlay, exprCtx)
	if err != nil {
		return nil, fmt.Errorf("resolve per-item config: %w", err)
	}
	if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
		profile, perr := vault.ProfileIDFromContext(ctx)
		if perr != nil {
			return nil, fmt.Errorf("db node ran without a profile: %w", perr)
		}
		if err := vault.ResolveConfig(ctx, vaultDB, overlay, resolved); err != nil {
			return nil, err
		}
		if err := secrets.ResolveConfig(ctx, vaultDB, profile, overlay, resolved); err != nil {
			return nil, err
		}
	}
	for k, v := range resolved {
		out[k] = v
	}
	return out, nil
}

// inputItems returns the node's input items, or one empty item when the node
// runs without input (e.g. right after a trigger), so per-item operations
// still run once.
func inputItems(input workflow.NodeInput) []workflow.Item {
	if len(input.Items) == 0 {
		return []workflow.Item{{JSON: map[string]interface{}{}}}
	}
	return input.Items
}

// firstItem returns the first input item (or an empty one). Legacy configs
// resolve their held-back fields against it, which is exactly what the engine
// did before those fields were declared per-item.
func firstItem(input workflow.NodeInput) workflow.Item {
	return inputItems(input)[0]
}

// cfgString returns config[key] as a trimmed string ("" when absent).
func cfgString(config map[string]interface{}, key string) string {
	switch v := config[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// cfgInt returns config[key] as an int, accepting JSON numbers and numeric
// strings. ok is false when the key is absent or not numeric.
func cfgInt(config map[string]interface{}, key string) (int, bool) {
	return toInt(config[key])
}

func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

// cfgBool returns config[key] as a bool (def when absent), accepting
// "true"/"false" strings as well.
func cfgBool(config map[string]interface{}, key string, def bool) bool {
	switch v := config[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

// toStringList accepts a JSON array, a []string, or a comma-separated string
// and returns the trimmed, non-empty entries.
func toStringList(v interface{}) []string {
	var out []string
	switch t := v.(type) {
	case []interface{}:
		for _, e := range t {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" && e != nil {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// toMap accepts a JSON object or a string holding one (the UI stores JSON
// textareas as strings). nil, "" and non-object values yield nil.
func toMap(v interface{}) (map[string]interface{}, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil, fmt.Errorf("expected a JSON object: %w", err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("expected a JSON object, got %T", v)
}

// toList accepts a JSON array or a string holding one.
func toList(v interface{}) ([]interface{}, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []interface{}:
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		var l []interface{}
		if err := json.Unmarshal([]byte(t), &l); err != nil {
			return nil, fmt.Errorf("expected a JSON array: %w", err)
		}
		return l, nil
	case map[string]interface{}:
		// n8n fixedCollection shape: {"values": [...]}
		if inner, ok := t["values"]; ok {
			return toList(inner)
		}
	}
	return nil, fmt.Errorf("expected a JSON array, got %T", v)
}

// sortedKeys returns m's keys in lexical order so generated statements are
// deterministic.
func sortedMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// normalizeValue converts driver values ([]byte) into JSON-friendly ones.
func normalizeValue(v interface{}) interface{} {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// mainOutputs packages the main/error item slices the way http.request does:
// a handle appears only when it has items, and a node with no output at all
// still returns an empty main handle.
func mainOutputs(main, errs []workflow.Item) []workflow.NodeOutput {
	var out []workflow.NodeOutput
	if len(main) > 0 || len(errs) == 0 {
		out = append(out, workflow.NodeOutput{Handle: "main", Items: main})
	}
	if len(errs) > 0 {
		out = append(out, workflow.NodeOutput{Handle: "error", Items: errs})
	}
	return out
}

// errorItem builds an error-handle item that keeps the failing input's JSON.
func errorItem(item workflow.Item, err error) workflow.Item {
	data := make(map[string]interface{}, len(item.JSON)+1)
	for k, v := range item.JSON {
		data[k] = v
	}
	data["error"] = err.Error()
	return workflow.NewItem(data)
}

// parseMessageBody decodes payload as JSON when jsonParse is set and it parses,
// otherwise returns it as a string.
func parseMessageBody(payload []byte, jsonParse bool) interface{} {
	if jsonParse {
		var v interface{}
		if json.Unmarshal(payload, &v) == nil {
			return v
		}
	}
	return string(payload)
}

// messagePayload returns the bytes a queue node sends for one item: the
// item's JSON when sendInputData is set, otherwise the configured message.
func messagePayload(item workflow.Item, sendInputData bool, message interface{}) ([]byte, error) {
	var b []byte
	if sendInputData {
		var err error
		b, err = json.Marshal(item.JSON)
		if err != nil {
			return nil, fmt.Errorf("encode item as JSON: %w", err)
		}
	} else {
		switch m := message.(type) {
		case string:
			b = []byte(m)
		case nil:
			b = nil
		default:
			var err error
			b, err = json.Marshal(m)
			if err != nil {
				return nil, fmt.Errorf("encode message as JSON: %w", err)
			}
		}
	}
	return b, nil
}

// toKVMap accepts a JSON object (or a string holding one) or n8n's list of
// {key|name|column, value} pairs and returns it as an object.
func toKVMap(v interface{}) (map[string]interface{}, error) {
	if m, ok := v.(map[string]interface{}); ok {
		for _, wrap := range []string{"values", "header", "headerValues", "argument", "attribute"} {
			if inner, ok := m[wrap].([]interface{}); ok && len(m) == 1 {
				return toKVMap(inner)
			}
		}
		return m, nil
	}
	list, isList := v.([]interface{})
	if !isList {
		return toMap(v)
	}
	out := make(map[string]interface{}, len(list))
	for i, e := range list {
		m, ok := e.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("entry %d: expected {key, value}", i)
		}
		k := cfgString(m, "key")
		if k == "" {
			k = cfgString(m, "name")
		}
		if k == "" {
			k = cfgString(m, "column")
		}
		if k == "" {
			return nil, fmt.Errorf("entry %d: missing key", i)
		}
		out[k] = m["value"]
	}
	return out, nil
}
