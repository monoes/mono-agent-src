package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

const (
	opRemoveInputDuplicates    = "removeDuplicateInputItems"
	opRemoveSeenInPreviousRuns = "removeItemsRepeatedWithinPreviousExecutions"
	opClearHistory             = "clearDeduplicationHistory"
)

// dedupeHistory opens the persisted seen-values history the cross-run
// operations share. Config fields:
//
//	"scope"       (string, optional): "node" (default) = this node's own history,
//	                                  "workflow" = shared by every node of the workflow
//	"historySize" (number, optional): values kept, least recently seen dropped
//	                                  first (default workflow.DefaultSeenHistorySize)
func dedupeHistory(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) (*workflow.SeenKeys, error) {
	scope, err := dedupeScope(config)
	if err != nil {
		return nil, err
	}
	var size int
	switch v := config["historySize"].(type) {
	case float64:
		size = int(v)
	case int:
		size = v
	}
	if size < 0 {
		return nil, fmt.Errorf("%w: remove_duplicates: 'historySize' must not be negative", workflow.ErrInvalidConfig)
	}
	seen, err := workflow.SeenKeysFor(ctx, input, scope, workflow.SeenOptions{HistorySize: size})
	if err != nil {
		return nil, fmt.Errorf("remove_duplicates: %w", err)
	}
	return seen, nil
}

// dedupeScope reads "scope": "node" (default) or "workflow".
func dedupeScope(config map[string]interface{}) (workflow.StateScope, error) {
	scope, _ := config["scope"].(string)
	switch scope {
	case "":
		return workflow.StateScopeNode, nil
	case string(workflow.StateScopeNode), string(workflow.StateScopeWorkflow):
		return workflow.StateScope(scope), nil
	default:
		return "", fmt.Errorf("%w: remove_duplicates: 'scope' must be \"node\" or \"workflow\", got %q", workflow.ErrInvalidConfig, scope)
	}
}

// removeSeenInPreviousRuns keeps the items whose dedupe value no earlier run
// (and no earlier item of this run) produced, and records every value.
// "dedupeValue" is an expression ({{ $json.id }}) or a dot-path field; when it
// is empty "field" is used, and when both are empty the whole item.
func removeSeenInPreviousRuns(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	switch logic, _ := config["logic"].(string); logic {
	case "", logicSeenValues:
	case logicIncremental, logicDate:
		return removeUpToStored(ctx, input, config, logic)
	default:
		return nil, fmt.Errorf("%w: remove_duplicates: unknown logic %q (want %q, %q or %q)", workflow.ErrInvalidConfig,
			logic, logicSeenValues, logicIncremental, logicDate)
	}
	seen, err := dedupeHistory(ctx, input, config)
	if err != nil {
		return nil, err
	}
	expr, _ := config["dedupeValue"].(string)
	if expr == "" {
		expr, _ = config["field"].(string)
	}
	engine := workflow.NewExpressionEngine()

	values := make([]string, len(input.Items))
	for i, item := range input.Items {
		v, err := dedupeValueOf(engine, expr, item, input)
		if err != nil {
			return nil, fmt.Errorf("remove_duplicates: item %d: %w", i, err)
		}
		values[i] = v
	}
	fresh, err := seen.Check(ctx, values)
	if err != nil {
		return nil, fmt.Errorf("remove_duplicates: %w", err)
	}
	result := make([]workflow.Item, 0, len(input.Items))
	for i, item := range input.Items {
		if fresh[i] {
			result = append(result, item)
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: result}}, nil
}

func dedupeValueOf(engine *workflow.ExpressionEngine, expr string, item workflow.Item, input workflow.NodeInput) (string, error) {
	switch {
	case expr == "":
		b, err := json.Marshal(item.JSON)
		if err != nil {
			return "", fmt.Errorf("failed to marshal item: %w", err)
		}
		return string(b), nil
	case strings.Contains(expr, "{{"):
		v, err := engine.EvaluateString(expr, workflow.ExpressionContext{
			JSON: item.JSON, Node: input.NodeOutputs, WorkflowID: input.WorkflowID, ExecutionID: input.ExecutionID,
		})
		if err != nil {
			return "", fmt.Errorf("dedupeValue: %w", err)
		}
		if v == "" {
			return "", fmt.Errorf("dedupeValue evaluated to an empty value")
		}
		return v, nil
	default:
		v := dotGet(item.JSON, expr)
		if v == nil || v == "" {
			return "", fmt.Errorf("field %q is missing or empty", expr)
		}
		if s, ok := v.(string); ok {
			return s, nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("field %q: %w", expr, err)
		}
		return string(b), nil
	}
}

// clearDedupeHistory forgets the scope's seen values and stored maximums
// and passes the items through.
func clearDedupeHistory(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	seen, err := dedupeHistory(ctx, input, config)
	if err != nil {
		return nil, err
	}
	if err := seen.Clear(ctx); err != nil {
		return nil, fmt.Errorf("remove_duplicates: %w", err)
	}
	scope, _ := dedupeScope(config) // dedupeHistory has checked it
	if err := clearWatermarks(ctx, input, scope); err != nil {
		return nil, fmt.Errorf("remove_duplicates: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: input.Items}}, nil
}
