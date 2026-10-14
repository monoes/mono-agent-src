package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SetNode sets or transforms fields on each item using template expressions.
// Config fields:
//
//	"assignments" ([]interface{}, required): list of assignment objects, each with:
//	    "field" (string): dot-path target key, e.g. "a.b.c"
//	    "value" (string): template expression to evaluate
//	    "type"  (string): conversion type — "string", "number", "bool", "json"
//	                      (also "boolean", "integer", "float", "array", "object")
//	"include_input" (bool, optional, default true): if false, output items contain only assigned fields
type SetNode struct{}

func (n *SetNode) Type() string { return "core.set" }

// PerItemConfigFields declares that each assignment's "value" must not be
// pre-resolved using only the first input item: Execute below evaluates it
// fresh for every item via engine.EvaluateValue.
func (n *SetNode) PerItemConfigFields() []string { return []string{"assignments[].value"} }

func (n *SetNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	var assignments []interface{}
	if rawAssignments, ok := config["assignments"]; ok {
		var isArray bool
		assignments, isArray = rawAssignments.([]interface{})
		if !isArray {
			return nil, fmt.Errorf("%w: set node \"assignments\" must be an array", workflow.ErrInvalidConfig)
		}
	} else {
		for k, v := range config {
			if k == "include_input" || k == "assignments" || strings.HasPrefix(k, "_") {
				continue
			}
			assignments = append(assignments, map[string]interface{}{
				"field": k,
				"value": fmt.Sprintf("%v", v),
				"type":  "string",
			})
		}
		if len(assignments) == 0 {
			return nil, fmt.Errorf("%w: set node requires \"assignments\"", workflow.ErrInvalidConfig)
		}
	}

	includeInput := true
	if v, exists := config["include_input"]; exists {
		if b, ok := v.(bool); ok {
			includeInput = b
		}
	}

	engine := workflow.NewExpressionEngine()
	outputItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		// Build the base map for the output item.
		base := make(map[string]interface{})
		if includeInput && item.JSON != nil {
			for k, v := range item.JSON {
				base[k] = v
			}
		}

		// Build the expression context for this item.
		exprCtx := input.ItemContext(item.JSON)

		for i, rawAssign := range assignments {
			assign, ok := rawAssign.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("%w: set node assignment[%d] must be an object", workflow.ErrInvalidConfig, i)
			}

			field, _ := assign["field"].(string)
			if field == "" {
				return nil, fmt.Errorf("%w: set node assignment[%d] missing \"field\"", workflow.ErrInvalidConfig, i)
			}

			typeName, _ := assign["type"].(string)
			if typeName == "" {
				typeName = "string"
			}

			// If the value was already resolved to a native Go type by ResolveConfig
			// (e.g. a []interface{} parsed from a JSON template result), use it directly.
			// Only call EvaluateValue when the value is still a raw template string.
			rawAssignVal := assign["value"]
			var rawVal interface{}
			var err error
			if valueExpr, isStr := rawAssignVal.(string); isStr {
				rawVal, err = engine.EvaluateValue(valueExpr, exprCtx)
				if err != nil {
					return nil, fmt.Errorf("set node assignment[%d] expression error: %w", i, err)
				}
			} else {
				// Already a resolved native value (map, slice, number, bool, etc.)
				rawVal = rawAssignVal
			}

			converted, err := convertType(rawVal, typeName)
			if err != nil {
				return nil, fmt.Errorf("set node assignment[%d] type conversion error: %w", i, err)
			}

			setNestedKey(base, field, converted)
		}

		outputItems = append(outputItems, workflow.Item{JSON: base})
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: outputItems},
	}, nil
}

// setNestedKey sets a value in a map using a dot-separated path, creating
// intermediate maps as needed.  For example, setNestedKey(m, "a.b.c", 42)
// ensures m["a"]["b"]["c"] == 42.
func setNestedKey(m map[string]interface{}, path string, val interface{}) {
	parts := strings.Split(path, ".")
	current := m
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = val
			return
		}
		// Descend or create the next level.
		next, exists := current[part]
		if !exists {
			child := make(map[string]interface{})
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]interface{})
		if !ok {
			// Overwrite non-map intermediate with a new map.
			child = make(map[string]interface{})
			current[part] = child
		}
		current = child
	}
}

// convertType coerces val into the requested type.
func convertType(val interface{}, typeName string) (interface{}, error) {
	// n8n's Set node spellings (imported workflows, agents that know n8n).
	switch strings.ToLower(typeName) {
	case "boolean":
		typeName = "bool"
	case "integer", "float":
		typeName = "number"
	case "array", "object":
		typeName = "json"
	}
	switch typeName {
	case "string":
		if val == nil {
			return "", nil
		}
		if s, ok := val.(string); ok {
			return s, nil
		}
		return fmt.Sprintf("%v", val), nil

	case "number":
		switch v := val.(type) {
		case float64:
			return v, nil
		case float32:
			return float64(v), nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case string:
			var f float64
			if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
				return nil, fmt.Errorf("cannot convert %q to number", v)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to number", val)
		}

	case "bool":
		switch v := val.(type) {
		case bool:
			return v, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes":
				return true, nil
			case "false", "0", "no", "":
				return false, nil
			default:
				return nil, fmt.Errorf("cannot convert %q to bool", v)
			}
		case float64:
			return v != 0, nil
		case int:
			return v != 0, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to bool", val)
		}

	case "json":
		// If the value is already a structured type, pass it through.
		switch val.(type) {
		case map[string]interface{}, []interface{}:
			return val, nil
		case string:
			var out interface{}
			if err := json.Unmarshal([]byte(val.(string)), &out); err != nil {
				return nil, fmt.Errorf("cannot parse JSON: %w", err)
			}
			return out, nil
		default:
			return val, nil
		}

	default:
		return val, nil
	}
}
