package image

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ImageVaultGetNode resolves a media vault id (image, audio or video) (e.g. "img-001", with or
// without the "@" ref prefix vault.Resolve expects) back into the absolute
// file path on disk, so downstream image.* nodes can operate on it.
type ImageVaultGetNode struct{}

func (n *ImageVaultGetNode) Type() string { return "image.vault_get" }

func (n *ImageVaultGetNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	if field == "" {
		field = "vault_id"
	}
	outputField, _ := config["output_field"].(string)
	if outputField == "" {
		outputField = "image_path"
	}

	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("image.vault_get: no database in context")
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		ref, _ := item.JSON[field].(string)
		if ref == "" {
			return nil, fmt.Errorf("image.vault_get: no vault id found in item field %q", field)
		}
		if !strings.HasPrefix(ref, "@") {
			ref = "@" + ref
		}

		path, err := vault.Resolve(ctx, db, ref)
		if err != nil {
			return nil, fmt.Errorf("image.vault_get: %w", err)
		}
		// vault.Resolve only reads the stored path from the DB — it never
		// checks the file is still there (e.g. manually deleted from
		// ~/.monoagent/.../vault/). Catch that here with a clear error
		// instead of handing downstream nodes a path that will fail with a
		// confusing "no such file" from whatever tries to open it next.
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("image.vault_get: vault image %q is registered but its file is missing (%s): %w", ref, path, err)
		}
		newJSON[outputField] = path
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}
