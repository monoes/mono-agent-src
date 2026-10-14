package control

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SwitchNode evaluates an expression and routes items to one of N output handles.
// Config fields:
//
//	"field" (string, required): value expression, e.g. "{{$json.status}}"
//	"cases" ([]interface{}, required): either plain strings (each string is both
//	  the match value and the output handle name) or map{"value": "pending", "handle": "case0"}
//	  for a custom handle name.
//	"default_handle" (string, optional): handle name for unmatched items, default "default"
//	"fallthrough" (bool, optional): if true, item can match multiple cases
type SwitchNode struct{}

func (n *SwitchNode) Type() string { return "core.switch" }

// PerItemConfigFields declares that "field" and "expression" must not be
// pre-resolved using only the first input item — they're evaluated fresh
// for every item below, so each item routes on its own $json rather than
// the whole batch collapsing to item[0]'s value.
func (n *SwitchNode) PerItemConfigFields() []string { return []string{"field", "expression"} }

// switchCase is the normalized form of one entry in the "cases" config array.
type switchCase struct {
	value  string
	handle string
}

// parseSwitchCases normalizes the "cases" config value. Each element is either
// a plain string (used as both match value and handle name) or a
// map{"value":..., "handle":...} object with an explicit handle name.
func parseSwitchCases(casesRaw []interface{}) []switchCase {
	cases := make([]switchCase, 0, len(casesRaw))
	for _, caseRaw := range casesRaw {
		switch v := caseRaw.(type) {
		case string:
			if v != "" {
				cases = append(cases, switchCase{value: v, handle: v})
			}
		case map[string]interface{}:
			handle, _ := v["handle"].(string)
			if handle == "" {
				continue
			}
			cases = append(cases, switchCase{value: fmt.Sprintf("%v", v["value"]), handle: handle})
		}
	}
	return cases
}

func (n *SwitchNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	expression, _ := config["field"].(string)
	if expression == "" {
		expression, _ = config["expression"].(string)
	}
	casesRawIface, _ := config["cases"].([]interface{})
	cases := parseSwitchCases(casesRawIface)
	defaultHandle, _ := config["default_handle"].(string)
	if defaultHandle == "" {
		defaultHandle = "default"
	}
	fallthroughMode, _ := config["fallthrough"].(bool)

	engine := workflow.NewExpressionEngine()

	// Collect results per handle: handle -> []Item
	handleItems := make(map[string][]workflow.Item)

	for _, item := range input.Items {
		exprCtx := input.ItemContext(item.JSON)

		val, err := engine.EvaluateString(expression, exprCtx)
		if err != nil {
			val = ""
		}

		matched := false
		for _, c := range cases {
			if val == c.value {
				handleItems[c.handle] = append(handleItems[c.handle], item)
				matched = true
				if !fallthroughMode {
					break
				}
			}
		}

		if !matched {
			handleItems[defaultHandle] = append(handleItems[defaultHandle], item)
		}
	}

	// Build ordered outputs: cases first (in order), then default if populated.
	seen := make(map[string]bool)
	var outputs []workflow.NodeOutput

	for _, c := range cases {
		if seen[c.handle] {
			continue
		}
		seen[c.handle] = true
		outputs = append(outputs, workflow.NodeOutput{
			Handle: c.handle,
			Items:  handleItems[c.handle],
		})
	}

	if !seen[defaultHandle] {
		outputs = append(outputs, workflow.NodeOutput{
			Handle: defaultHandle,
			Items:  handleItems[defaultHandle],
		})
	}

	return outputs, nil
}
