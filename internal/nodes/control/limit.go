package control

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// LimitNode keeps the first N items from the input.
// Config fields:
//
//	"max_items" (int or float, required): maximum number of items to keep (1–10000)
type LimitNode struct{}

func (n *LimitNode) Type() string { return "core.limit" }

// PublishesToThirdParty: core.limit sends nothing off this machine (issue #1418).
func (LimitNode) PublishesToThirdParty() bool { return false }

func (n *LimitNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	maxRaw, ok := config["max_items"]
	if !ok {
		return nil, fmt.Errorf("limit: 'max_items' config is required")
	}

	maxFloat, err := toFloat(maxRaw)
	if err != nil {
		return nil, fmt.Errorf("limit: 'max_items' must be a number: %w", err)
	}
	maxItems := int(maxFloat)
	if maxItems < 1 || maxItems > 10000 {
		return nil, fmt.Errorf("limit: 'max_items' must be between 1 and 10000, got %d", maxItems)
	}

	end := maxItems
	if end > len(input.Items) {
		end = len(input.Items)
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: input.Items[:end]},
	}, nil
}
