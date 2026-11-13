package data

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// DateTimeNode performs date/time operations on item fields.
// Type: "data.datetime"
type DateTimeNode struct{}

func (n *DateTimeNode) Type() string { return "data.datetime" }

// PublishesToThirdParty: data.datetime sends nothing off this machine (issue #1418).
func (DateTimeNode) PublishesToThirdParty() bool { return false }

func (n *DateTimeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	operation, _ := config["operation"].(string)
	if alias, ok := N8nDateTimeOperations[operation]; ok {
		operation = alias
	}
	if dateTimeV2Ops[operation] {
		return n.executeV2(input, config, operation)
	}
	field, _ := config["field"].(string)
	inputFormat, _ := config["input_format"].(string)
	outputFormat, _ := config["output_format"].(string)
	durationStr, _ := config["duration"].(string)
	outputField, _ := config["output_field"].(string)

	if inputFormat == "" {
		inputFormat = time.RFC3339
	}
	if outputFormat == "" {
		outputFormat = time.RFC3339
	}
	if outputField == "" {
		outputField = field
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		switch operation {
		case "format":
			raw, _ := newJSON[field].(string)
			t, err := time.Parse(inputFormat, raw)
			if err != nil {
				return nil, fmt.Errorf("data.datetime format: parse field %q: %w", field, err)
			}
			newJSON[outputField] = t.Format(outputFormat)

		case "parse":
			raw, _ := newJSON[field].(string)
			t, err := time.Parse(inputFormat, raw)
			if err != nil {
				return nil, fmt.Errorf("data.datetime parse: parse field %q: %w", field, err)
			}
			newJSON[outputField] = t.Unix()

		case "add":
			raw, _ := newJSON[field].(string)
			t, err := time.Parse(inputFormat, raw)
			if err != nil {
				return nil, fmt.Errorf("data.datetime add: parse field %q: %w", field, err)
			}
			dur, err := time.ParseDuration(durationStr)
			if err != nil {
				return nil, fmt.Errorf("data.datetime add: parse duration %q: %w", durationStr, err)
			}
			newJSON[outputField] = t.Add(dur).Format(outputFormat)

		case "subtract":
			raw, _ := newJSON[field].(string)
			t, err := time.Parse(inputFormat, raw)
			if err != nil {
				return nil, fmt.Errorf("data.datetime subtract: parse field %q: %w", field, err)
			}
			dur, err := time.ParseDuration(durationStr)
			if err != nil {
				return nil, fmt.Errorf("data.datetime subtract: parse duration %q: %w", durationStr, err)
			}
			newJSON[outputField] = t.Add(-dur).Format(outputFormat)

		case "diff":
			field2, _ := config["field2"].(string)
			raw1, _ := newJSON[field].(string)
			raw2, _ := newJSON[field2].(string)
			t1, err := time.Parse(inputFormat, raw1)
			if err != nil {
				return nil, fmt.Errorf("data.datetime diff: parse field %q: %w", field, err)
			}
			t2, err := time.Parse(inputFormat, raw2)
			if err != nil {
				return nil, fmt.Errorf("data.datetime diff: parse field2 %q: %w", field2, err)
			}
			newJSON[outputField] = t1.Sub(t2).Seconds()

		case "now":
			newJSON[outputField] = time.Now().Format(outputFormat)

		default:
			return nil, fmt.Errorf("data.datetime: unknown operation %q", operation)
		}

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// copyMap performs a shallow copy of a map[string]interface{}.
func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
