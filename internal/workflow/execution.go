package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/vault"
)

// executionResumeState is the serialized snapshot persisted when an execution
// pauses (WAITING) and restored to continue it. pendingInputs already captures
// per-handle routing (items keyed by target node ID), so no per-handle output
// schema is needed to resume correctly.
type executionResumeState struct {
	PendingInputs map[string][]Item `json:"pending_inputs"`
	// PendingFrom names the node that fed each pending input ($prevNode).
	PendingFrom    map[string]string `json:"pending_from,omitempty"`
	NodeOutputs    map[string][]Item `json:"node_outputs"`
	CompletedNodes map[string]bool   `json:"completed_nodes"`
	MergeWaiting   map[string]int    `json:"merge_waiting"`
	// NodeState is each node's private state (SetNodeState) by node ID,
	// e.g. a loop node's remaining items or a durable wait's target time.
	NodeState map[string]json.RawMessage `json:"node_state,omitempty"`
	// Loops holds the active loop nodes' engine bookkeeping by node ID.
	Loops map[string]*loopState `json:"loops,omitempty"`
	// ActiveMs is the run's active (not WAITING) time so far, counted
	// against the workflow's executionTimeout.
	ActiveMs int64 `json:"active_ms,omitempty"`
}

// dbCtx returns a short-lived context for DB persistence operations.
// It is intentionally independent of the execution context so that
// persistence succeeds even when the execution has been cancelled.
func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// buildExpressionContext constructs an ExpressionContext from the current
// item's JSON map and the accumulated nodeOutputs map. $trigger is the
// execution's trigger data and $vars the workflow's settings.variables map.
func buildExpressionContext(
	currentJSON map[string]interface{},
	nodeOutputs map[string][]Item,
	wf *Workflow,
	exec *WorkflowExecution,
) ExpressionContext {
	if currentJSON == nil {
		currentJSON = make(map[string]interface{})
	}
	// Shallow copy nodeOutputs so the context is not mutated by later nodes.
	nodeCopy := make(map[string][]Item, len(nodeOutputs))
	for k, v := range nodeOutputs {
		nodeCopy[k] = v
	}
	vars, _ := wf.Settings["variables"].(map[string]interface{})
	return ExpressionContext{
		JSON:        currentJSON,
		Node:        nodeCopy,
		WorkflowID:  wf.ID,
		ExecutionID: exec.ID,
		Env:         nil, // populated from ctx.Env; OS env only merged when MONOAGENT_ALLOW_ENV_TEMPLATES=1
		Trigger:     exec.TriggerData,
		Vars:        vars,
	}
}

// buildTriggerItems wraps trigger data as a single Item, or expands a
// sub-workflow call's SubWorkflowItemsKey into its items.
// If triggerData is nil or empty, a single empty Item is returned so
// trigger nodes always receive at least one item to process.
func buildTriggerItems(triggerData map[string]interface{}) []Item {
	if triggerData == nil {
		triggerData = make(map[string]interface{})
	}
	if items, ok := subWorkflowTriggerItems(triggerData); ok {
		return items
	}
	item := NewItem(triggerData)
	item.Binary = webhookFiles(triggerData)
	return []Item{item}
}

// webhookFiles decodes the multipart files a webhook item carries under
// "binary" (field -> {file_name, mime_type, file_size, data (base64)}, see
// buildWebhookItem) back into bytes for Item.Binary: trigger data is stored
// as JSON, which keeps only that form. Anything not shaped like it (a body
// field named "binary", a manual run's data) yields nil.
func webhookFiles(triggerData map[string]interface{}) map[string][]byte {
	files, ok := triggerData["binary"].(map[string]interface{})
	if !ok || len(files) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(files))
	for field, v := range files {
		f, _ := v.(map[string]interface{})
		enc, _ := f["data"].(string)
		data, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || !sameSize(f["file_size"], len(data)) {
			return nil
		}
		out[field] = data
	}
	return out
}

// sameSize reports whether a file_size value (an int, or a float64 after a
// JSON round trip) is n.
func sameSize(v interface{}, n int) bool {
	switch s := v.(type) {
	case int:
		return s == n
	case float64:
		return s == float64(n)
	}
	return false
}

// extractOnError reads the on_error string from the resolved config map.
// Defaults to "stop".
func extractOnError(config map[string]interface{}) string {
	if v, ok := config["on_error"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "stop"
}

// errorHandleMessage extracts a human-readable failure message from the
// items a node routed to its "error" output. The convention is an "error"
// JSON field (http.request, system.execute_command emit it); items without
// one yield "" so the caller can fall back to a generic message.
func errorHandleMessage(items []Item) string {
	for _, it := range items {
		if it.JSON == nil {
			continue
		}
		if msg, ok := it.JSON["error"].(string); ok && msg != "" {
			return msg
		}
	}
	return ""
}

// perItemFieldState is the token extractPerItemFields returns, holding the
// raw values it removed from a node's config so restorePerItemFields can put
// them back unresolved after expr.ResolveConfig runs.
type perItemFieldState struct {
	topLevel map[string]interface{} // top-level key -> saved raw value
	nested   []nestedPerItemField
}

// nestedPerItemField holds the raw values saved from one sub-key (e.g.
// "value") across every element of one top-level array field (e.g.
// "assignments") that extractPerItemFields protected.
type nestedPerItemField struct {
	arrayKey string
	subKey   string
	saved    map[int]interface{} // element index -> saved raw value
}

// parsePerItemFieldSpec splits a PerItemConfigResolver field spec into an
// array key and sub-key, e.g. "assignments[].value" -> ("assignments",
// "value"). A spec with no "[]." is a plain top-level field; arrayKey is
// returned empty and key holds the spec itself.
func parsePerItemFieldSpec(spec string) (arrayKey, key string) {
	if idx := strings.Index(spec, "[]."); idx >= 0 {
		return spec[:idx], spec[idx+3:]
	}
	return "", spec
}

// extractPerItemFields removes the config fields named by specs — see
// PerItemConfigResolver — from config in place, so the caller's subsequent
// expr.ResolveConfig pass (which only sees the first input item) leaves them
// untouched. restorePerItemFields puts the raw values back afterward so the
// node itself can resolve them once per item.
func extractPerItemFields(config map[string]interface{}, specs []string) *perItemFieldState {
	state := &perItemFieldState{topLevel: make(map[string]interface{})}
	for _, spec := range specs {
		arrayKey, key := parsePerItemFieldSpec(spec)
		if arrayKey == "" {
			if v, ok := config[key]; ok {
				state.topLevel[key] = v
				delete(config, key)
			}
			continue
		}
		rawArr, ok := config[arrayKey].([]interface{})
		if !ok {
			continue
		}
		nested := nestedPerItemField{arrayKey: arrayKey, subKey: key, saved: make(map[int]interface{})}
		for i, rawElem := range rawArr {
			elem, ok := rawElem.(map[string]interface{})
			if !ok {
				continue
			}
			if v, ok := elem[key]; ok {
				nested.saved[i] = v
				delete(elem, key)
			}
		}
		state.nested = append(state.nested, nested)
	}
	return state
}

// restorePerItemFields writes the raw values extractPerItemFields removed
// back into resolvedConfig, unresolved. resolvedConfig is expr.ResolveConfig's
// output: a freshly built map/slice tree of the same shape (and, for arrays,
// the same length and order) as the config extractPerItemFields was called
// on, so the saved element indices still line up.
func restorePerItemFields(resolvedConfig map[string]interface{}, state *perItemFieldState) {
	for k, v := range state.topLevel {
		resolvedConfig[k] = v
	}
	for _, nested := range state.nested {
		rawArr, ok := resolvedConfig[nested.arrayKey].([]interface{})
		if !ok {
			continue
		}
		for i, v := range nested.saved {
			if i < 0 || i >= len(rawArr) {
				continue
			}
			elem, ok := rawArr[i].(map[string]interface{})
			if !ok {
				continue
			}
			elem[nested.subKey] = v
		}
	}
}

// autoRegisterItemImages checks if an output item contains an unregistered image file
// path in its JSON (e.g. file_path or image_path) and registers it in the image vault.
func autoRegisterItemImages(ctx context.Context, outputs []NodeOutput, wfID, execID string) {
	vaultDB := vault.DBFromContext(ctx)
	if vaultDB == nil {
		return
	}
	for _, out := range outputs {
		for _, item := range out.Items {
			if item.JSON == nil {
				continue
			}
			if vid, ok := item.JSON["vault_id"].(string); ok && vid != "" {
				continue
			}
			for _, key := range []string{"file_path", "image_path"} {
				p, ok := item.JSON[key].(string)
				if !ok || p == "" {
					continue
				}
				if !imagescan.IsImageFile(p) {
					continue
				}
				fi, err := os.Stat(p)
				if err != nil || fi.IsDir() {
					continue
				}
				if vaultID, err := vault.Register(ctx, vaultDB, p, "workflow", wfID, execID); err == nil {
					item.JSON["vault_id"] = vaultID
					break
				}
			}
		}
	}
}
