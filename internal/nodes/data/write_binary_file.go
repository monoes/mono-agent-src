package data

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// WriteBinaryFileNode writes binary or text content from an item field to a file.
// Type: "data.write_binary_file"
type WriteBinaryFileNode struct{}

func (n *WriteBinaryFileNode) Type() string { return "data.write_binary_file" }

// PublishesToThirdParty: data.write_binary_file sends nothing off this machine (issue #1418).
func (WriteBinaryFileNode) PublishesToThirdParty() bool { return false }

func (n *WriteBinaryFileNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	filePath, _ := config["file_path"].(string)
	if filePath == "" {
		return nil, fmt.Errorf("data.write_binary_file: file_path is required")
	}
	filePath, err := fsconfine.WritePath(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("data.write_binary_file: %w", err)
	}

	field, _ := config["field"].(string)
	encoding, _ := config["encoding"].(string)
	if encoding == "" {
		encoding = "base64"
	}

	createDirs := true
	if v, ok := config["create_dirs"].(bool); ok {
		createDirs = v
	}

	if createDirs {
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("data.write_binary_file: create dirs for %q: %w", filePath, err)
		}
	}

	var src string
	if len(input.Items) > 0 {
		src, _ = input.Items[0].JSON[field].(string)
	}

	var content []byte

	switch encoding {
	case "base64":
		content, err = base64.StdEncoding.DecodeString(src)
		if err != nil {
			return nil, fmt.Errorf("data.write_binary_file: base64 decode field %q: %w", field, err)
		}
	case "utf8":
		content = []byte(src)
	default:
		return nil, fmt.Errorf("data.write_binary_file: unknown encoding %q", encoding)
	}

	if err := os.WriteFile(filePath, content, 0644); err != nil {
		return nil, fmt.Errorf("data.write_binary_file: write %q: %w", filePath, err)
	}

	outItem := workflow.NewItem(map[string]interface{}{
		"bytes_written": len(content),
		"file_path":     filePath,
	})

	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{outItem}}}, nil
}
