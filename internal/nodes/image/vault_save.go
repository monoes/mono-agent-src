package image

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ImageVaultSaveNode copies an item's image, audio or video file into the
// profile's media vault (~/.monoagent/vault/), so it survives independently of wherever the
// source file lives (a temp dir, a downloaded file, another node's output)
// and can be referenced later by id from any workflow.
type ImageVaultSaveNode struct{}

func (n *ImageVaultSaveNode) Type() string { return "image.vault_save" }

func (n *ImageVaultSaveNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	source, _ := config["source"].(string)
	if source == "" {
		source = "workflow"
	}
	outputField, _ := config["output_field"].(string)
	if outputField == "" {
		outputField = "vault_id"
	}

	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("image.vault_save: no database in context")
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.vault_save: %w", err)
		}
		if imgPath == "" {
			return nil, fmt.Errorf("image.vault_save: no image path found in item")
		}

		id, err := vault.Register(ctx, db, imgPath, source, input.WorkflowID, input.ExecutionID)
		if err != nil {
			return nil, fmt.Errorf("image.vault_save: %w", err)
		}
		newJSON[outputField] = id
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}
