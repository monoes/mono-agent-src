package control

import (
	"context"
	"fmt"
	"math"

	"github.com/monoes/mono-agent/internal/workflow"
)

// AggregateNode groups items and computes aggregate operations per group.
// Config fields:
//
//	"group_by"   (string, optional): dot-path field to group by. If empty, all items form one group.
//	"operations" ([]interface{}, required): list of operation descriptors, each a map with:
//	               "field"        (string): source field for the operation
//	               "operation"    (string): one of sum|count|avg|min|max|first|last|array
//	               "output_field" (string): key name in the output item
type AggregateNode struct{}

func (n *AggregateNode) Type() string { return "core.aggregate" }

type aggOp struct {
	field       string
	operation   string
	outputField string
}

// collectOpAliases maps a schema-facing operation name to the internal name
// computeOp understands, for names that differ between the two.
var collectOpAliases = map[string]string{"collect": "array"}

// parseAggregateOps builds the list of aggregate operations to run, supporting
// two config shapes:
//   - the flat single-operation schema (core.aggregate.json): "operation",
//     "field", "output_field" as top-level config keys.
//   - the multi-operation array ("operations": []map{field, operation,
//     output_field}), for callers that need more than one aggregate per group.
func parseAggregateOps(config map[string]interface{}) ([]aggOp, error) {
	opsRaw, ok := config["operations"].([]interface{})
	if !ok || len(opsRaw) == 0 {
		operation, _ := config["operation"].(string)
		if operation == "" {
			// The schema's default: `workflow validate` treats a required
			// field with a default as set, so the run must apply it too.
			operation = "count"
		}
		if alias, ok := collectOpAliases[operation]; ok {
			operation = alias
		}
		field, _ := config["field"].(string)
		outputField, _ := config["output_field"].(string)
		if outputField == "" {
			outputField = operation
		}
		return []aggOp{{field: field, operation: operation, outputField: outputField}}, nil
	}

	ops := make([]aggOp, 0, len(opsRaw))
	for i, raw := range opsRaw {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("aggregate: operation[%d] must be an object", i)
		}
		operation := fmt.Sprintf("%v", m["operation"])
		if alias, ok := collectOpAliases[operation]; ok {
			operation = alias
		}
		op := aggOp{
			field:       fmt.Sprintf("%v", m["field"]),
			operation:   operation,
			outputField: fmt.Sprintf("%v", m["output_field"]),
		}
		if op.operation == "" || op.operation == "<nil>" {
			return nil, fmt.Errorf("aggregate: operation[%d] missing 'operation'", i)
		}
		if op.outputField == "" || op.outputField == "<nil>" {
			return nil, fmt.Errorf("aggregate: operation[%d] missing 'output_field'", i)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func (n *AggregateNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	groupBy, _ := config["group_by"].(string)

	ops, err := parseAggregateOps(config)
	if err != nil {
		return nil, err
	}

	// Group items — preserve insertion order of group keys
	type group struct {
		key   string
		items []workflow.Item
	}
	groupMap := make(map[string]*group)
	groupOrder := make([]string, 0)

	const singleGroupKey = "__all__"

	for _, item := range input.Items {
		var key string
		if groupBy == "" {
			key = singleGroupKey
		} else {
			key = fmt.Sprintf("%v", dotGet(item.JSON, groupBy))
		}
		if _, exists := groupMap[key]; !exists {
			groupMap[key] = &group{key: key, items: make([]workflow.Item, 0)}
			groupOrder = append(groupOrder, key)
		}
		groupMap[key].items = append(groupMap[key].items, item)
	}

	result := make([]workflow.Item, 0, len(groupOrder))
	for _, key := range groupOrder {
		g := groupMap[key]
		outJSON := make(map[string]interface{})

		// Add group key to output
		if groupBy != "" {
			outJSON[groupBy] = key
		}

		for _, op := range ops {
			val, err := computeOp(g.items, op)
			if err != nil {
				return nil, fmt.Errorf("aggregate: group %q, operation %q on field %q: %w", key, op.operation, op.field, err)
			}
			outJSON[op.outputField] = val
		}

		result = append(result, workflow.NewItem(outJSON))
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: result},
	}, nil
}

// computeOp computes a single aggregate operation over a group of items.
func computeOp(items []workflow.Item, op aggOp) (interface{}, error) {
	switch op.operation {
	case "count":
		return float64(len(items)), nil

	case "sum":
		var sum float64
		for _, item := range items {
			v := dotGet(item.JSON, op.field)
			f, err := toFloat(v)
			if err != nil {
				continue // treat non-numeric as 0
			}
			sum += f
		}
		return sum, nil

	case "avg":
		if len(items) == 0 {
			return 0.0, nil
		}
		var sum float64
		count := 0
		for _, item := range items {
			v := dotGet(item.JSON, op.field)
			f, err := toFloat(v)
			if err != nil {
				continue
			}
			sum += f
			count++
		}
		if count == 0 {
			return 0.0, nil
		}
		return sum / float64(count), nil

	case "min":
		minVal := math.MaxFloat64
		found := false
		for _, item := range items {
			v := dotGet(item.JSON, op.field)
			f, err := toFloat(v)
			if err != nil {
				continue
			}
			if !found || f < minVal {
				minVal = f
				found = true
			}
		}
		if !found {
			return nil, nil
		}
		return minVal, nil

	case "max":
		maxVal := -math.MaxFloat64
		found := false
		for _, item := range items {
			v := dotGet(item.JSON, op.field)
			f, err := toFloat(v)
			if err != nil {
				continue
			}
			if !found || f > maxVal {
				maxVal = f
				found = true
			}
		}
		if !found {
			return nil, nil
		}
		return maxVal, nil

	case "first":
		if len(items) == 0 {
			return nil, nil
		}
		return dotGet(items[0].JSON, op.field), nil

	case "last":
		if len(items) == 0 {
			return nil, nil
		}
		return dotGet(items[len(items)-1].JSON, op.field), nil

	case "array":
		arr := make([]interface{}, 0, len(items))
		for _, item := range items {
			arr = append(arr, dotGet(item.JSON, op.field))
		}
		return arr, nil

	default:
		return nil, fmt.Errorf("unknown operation %q; must be one of sum|count|avg|min|max|first|last|array", op.operation)
	}
}
