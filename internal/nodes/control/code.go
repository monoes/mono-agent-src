package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/monoes/mono-agent/internal/workflow"
)

// CodeNode executes JavaScript code via the goja runtime.
// Config fields:
//
//	"code" (string, required): JavaScript code to execute.
//	    The JS environment exposes:
//	    - $input: all() returning all items as JS objects, first(),
//	      last(), and item (the first item)
//	    - $json: first item's JSON map (shorthand)
//	    - $('Node Name'): that node's output, with $input's accessors
//	    - $execution: id, resumeUrl and resumeFormUrl; $workflow: id
//	    The code must return an array of objects (or a single object), or
//	    a promise of one; a top-level return or await runs the code as an
//	    async function body.
//	    Each returned object becomes an Item on the "main" handle.
//	"timeout_seconds" (number, optional): execution timeout, default 30,
//	    clamped to 1-600.
//	"memory_limit_mb" (number, optional): JS runtime memory limit,
//	    default 512. Applied via goja's SetMemoryLimit when the vendored
//	    goja supports it; the per-item and item-count caps below are
//	    enforced regardless.
type CodeNode struct{}

// Caps on what a code node may return. A node exceeding any of these
// fails with an error naming the cap instead of flooding downstream
// nodes, storage, and execution summaries.
const (
	codeDefaultTimeoutSecs = 30
	codeMinTimeoutSecs     = 1
	codeMaxTimeoutSecs     = 600
	codeDefaultMemLimitMB  = 512
	codeMaxItems           = 10000
	codeMaxItemBytes       = 16 << 20 // 16MB per marshaled item
)

func (n *CodeNode) Type() string { return "core.code" }

func (n *CodeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	code, _ := config["code"].(string)
	if code == "" {
		return nil, fmt.Errorf("%w: code node requires \"code\"", workflow.ErrInvalidConfig)
	}

	rt := goja.New()

	memLimitMB := codeDefaultMemLimitMB
	if v := configNumber(config, "memory_limit_mb"); v > 0 {
		memLimitMB = int(v)
	}
	// The vendored goja revision has no SetMemoryLimit; the assertion
	// enables it automatically should a future goja grow one.
	if limiter, ok := interface{}(rt).(interface{ SetMemoryLimit(int) }); ok {
		limiter.SetMemoryLimit(memLimitMB << 20)
	}

	// $input with all(), first(), last() and item over the input items.
	inputObj := rt.NewObject()
	if err := setItemAccessors(rt, inputObj, jsonMaps(input.Items)); err != nil {
		return nil, fmt.Errorf("code node: failed to set $input: %w", err)
	}
	if err := rt.Set("$input", inputObj); err != nil {
		return nil, fmt.Errorf("code node: failed to set $input: %w", err)
	}
	// items: the input items as {json} objects, which n8n's Code node (and its
	// Function node before it) still offers next to $input.all().
	if _, err := rt.RunString("var items = $input.all().map(function (j) { return {json: j}; });"); err != nil {
		return nil, fmt.Errorf("code node: failed to set items: %w", err)
	}
	if err := setNodeAccessor(rt, input.NodeOutputs); err != nil {
		return nil, fmt.Errorf("code node: failed to set $: %w", err)
	}

	// $json — first item's JSON, or empty map.
	firstJSON := make(map[string]interface{})
	if len(input.Items) > 0 && input.Items[0].JSON != nil {
		for k, v := range input.Items[0].JSON {
			firstJSON[k] = v
		}
	}
	if err := rt.Set("$json", firstJSON); err != nil {
		return nil, fmt.Errorf("code node: failed to set $json: %w", err)
	}

	// $execution and $workflow, as in n8n's Code node. The resume URL is
	// signed, so it is only built for code that reads $execution.
	if strings.Contains(code, "$execution") {
		if err := rt.Set("$execution", workflow.ExecutionData(input.ExecutionID)); err != nil {
			return nil, fmt.Errorf("code node: failed to set $execution: %w", err)
		}
	}
	if err := rt.Set("$workflow", map[string]interface{}{"id": input.WorkflowID}); err != nil {
		return nil, fmt.Errorf("code node: failed to set $workflow: %w", err)
	}

	// Enforce the timeout via goja's interrupt mechanism.
	timeoutSecs := codeDefaultTimeoutSecs
	if v := configNumber(config, "timeout_seconds"); v > 0 {
		timeoutSecs = int(v)
	}
	if timeoutSecs < codeMinTimeoutSecs {
		timeoutSecs = codeMinTimeoutSecs
	}
	if timeoutSecs > codeMaxTimeoutSecs {
		timeoutSecs = codeMaxTimeoutSecs
	}
	timer := time.AfterFunc(time.Duration(timeoutSecs)*time.Second, func() {
		rt.Interrupt(fmt.Sprintf("code node: execution timeout (%ds)", timeoutSecs))
	})
	defer timer.Stop()

	// Also respect the parent context cancellation.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt("code node: context cancelled")
		case <-done:
		}
	}()

	prg, err := compileCode(code)
	var val goja.Value
	if err == nil {
		val, err = rt.RunProgram(prg)
	}
	if err == nil {
		val, err = settle(val)
	}
	close(done)

	if err != nil {
		// Translate context cancellation into the canonical error.
		if ctx.Err() != nil {
			return nil, workflow.ErrExecutionCancelled
		}
		return nil, fmt.Errorf("code node: JS execution error: %w", err)
	}

	// Convert the returned value to []workflow.Item.
	items, err := jsValueToItems(val)
	if err != nil {
		return nil, fmt.Errorf("code node: result conversion error: %w", err)
	}

	// goja's Export preserves reference cycles, and a returned object may
	// hold values encoding/json rejects. Validate every item is
	// JSON-serializable BEFORE returning it: a cyclic or otherwise
	// unmarshalable result is a node error, not a downstream crash.
	if err := validateItems(items); err != nil {
		return nil, err
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: items},
	}, nil
}

// configNumber reads a numeric config field whether it arrived as
// float64 (JSON-decoded) or an int (constructed in Go).
func configNumber(config map[string]interface{}, key string) float64 {
	switch v := config[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

// validateItems enforces the code-node output caps: at most codeMaxItems
// items, each JSON-marshalable and at most codeMaxItemBytes when
// marshaled. Errors name the cap that was hit.
func validateItems(items []workflow.Item) error {
	if len(items) > codeMaxItems {
		return fmt.Errorf("code node returned %d items, exceeding the cap of %d", len(items), codeMaxItems)
	}
	for i, it := range items {
		b, err := json.Marshal(it.JSON)
		if err != nil {
			return fmt.Errorf("code node returned non-serializable items (result[%d]: %v)", i, err)
		}
		if len(b) > codeMaxItemBytes {
			return fmt.Errorf("code node result[%d] is %d bytes when marshaled, exceeding the per-item cap of %d bytes", i, len(b), codeMaxItemBytes)
		}
	}
	return nil
}

// jsValueToItems converts a goja.Value (array of objects or single object)
// to a slice of workflow.Item.
func jsValueToItems(val goja.Value) ([]workflow.Item, error) {
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return []workflow.Item{}, nil
	}

	exported := val.Export()

	switch v := exported.(type) {
	case []interface{}:
		items := make([]workflow.Item, 0, len(v))
		for i, elem := range v {
			m, err := toStringMap(elem)
			if err != nil {
				return nil, fmt.Errorf("result[%d]: %w", i, err)
			}
			items = append(items, workflow.Item{JSON: n8nItemJSON(m)})
		}
		return items, nil

	case map[string]interface{}:
		return []workflow.Item{{JSON: n8nItemJSON(v)}}, nil

	default:
		return nil, fmt.Errorf("code must return an array of objects or a single object, got %T", exported)
	}
}

// n8nItemJSON unwraps an item written the n8n way, {json: {...}} (with
// optional binary/pairedItem, which have no place in a mono item), to its
// JSON; any other object is the item's JSON itself.
func n8nItemJSON(m map[string]interface{}) map[string]interface{} {
	j, ok := m["json"].(map[string]interface{})
	if !ok {
		return m
	}
	for k := range m {
		if k != "json" && k != "binary" && k != "pairedItem" {
			return m
		}
	}
	return j
}

// toStringMap casts an interface{} to map[string]interface{}.
func toStringMap(v interface{}) (map[string]interface{}, error) {
	if m, ok := v.(map[string]interface{}); ok {
		return m, nil
	}
	return nil, fmt.Errorf("expected object, got %T", v)
}
