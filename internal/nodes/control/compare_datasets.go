package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// CompareDatasetsNode diffs two item sets passed as a combined slice.
// Config fields:
//
//	"key_field"  (string, required): field used as unique identifier for each item
//	"dataset_a"  (string, optional): label for first dataset; default "main"
//	"dataset_b"  (string, optional): label for second dataset; default "input2"
//	"split_at"   (int, optional): index at which to split input.Items into A and B.
//	              If not set, splits 50/50.
//
// Output handles: "added", "removed", "changed", "unchanged"
type CompareDatasetsNode struct{}

func (n *CompareDatasetsNode) Type() string { return "core.compare_datasets" }

// PublishesToThirdParty: core.compare_datasets sends nothing off this machine (issue #1418).
func (CompareDatasetsNode) PublishesToThirdParty() bool { return false }

func (n *CompareDatasetsNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	keyField, _ := config["key_field"].(string)
	if keyField == "" {
		return nil, fmt.Errorf("compare_datasets: 'key_field' config is required")
	}

	total := len(input.Items)
	splitAt := total / 2
	if raw, ok := config["split_at"]; ok {
		f, err := toFloat(raw)
		if err != nil {
			return nil, fmt.Errorf("compare_datasets: 'split_at' must be a number: %w", err)
		}
		splitAt = int(f)
		if splitAt < 0 {
			splitAt = 0
		}
		if splitAt > total {
			splitAt = total
		}
	}

	datasetA := input.Items[:splitAt]
	datasetB := input.Items[splitAt:]

	// Index dataset A by key
	type entry struct {
		item    workflow.Item
		jsonStr string
	}
	indexA := make(map[string]entry, len(datasetA))
	for _, item := range datasetA {
		k := fmt.Sprintf("%v", dotGet(item.JSON, keyField))
		b, err := json.Marshal(item.JSON)
		if err != nil {
			return nil, fmt.Errorf("compare_datasets: failed to marshal item: %w", err)
		}
		indexA[k] = entry{item: item, jsonStr: string(b)}
	}

	// Index dataset B by key
	indexB := make(map[string]entry, len(datasetB))
	for _, item := range datasetB {
		k := fmt.Sprintf("%v", dotGet(item.JSON, keyField))
		b, err := json.Marshal(item.JSON)
		if err != nil {
			return nil, fmt.Errorf("compare_datasets: failed to marshal item: %w", err)
		}
		indexB[k] = entry{item: item, jsonStr: string(b)}
	}

	added := make([]workflow.Item, 0)
	removed := make([]workflow.Item, 0)
	changed := make([]workflow.Item, 0)
	unchanged := make([]workflow.Item, 0)

	// Items in B: added or changed/unchanged relative to A
	for k, eb := range indexB {
		ea, inA := indexA[k]
		if !inA {
			added = append(added, eb.item)
		} else if ea.jsonStr != eb.jsonStr {
			changed = append(changed, eb.item)
		} else {
			unchanged = append(unchanged, eb.item)
		}
	}

	// Items in A not in B: removed
	for k, ea := range indexA {
		if _, inB := indexB[k]; !inB {
			removed = append(removed, ea.item)
		}
	}

	return []workflow.NodeOutput{
		{Handle: "added", Items: added},
		{Handle: "removed", Items: removed},
		{Handle: "changed", Items: changed},
		{Handle: "unchanged", Items: unchanged},
	}, nil
}
