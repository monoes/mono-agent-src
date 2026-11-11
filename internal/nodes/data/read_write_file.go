package data

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ReadWriteFileNode reads files from disk into items (one item per file
// matching a pattern) or writes an item's file to disk (n8n's "Read/Write
// Files from Disk"). Paths are confined to the org workdir in confined runs.
// Type: "data.read_write_file"
type ReadWriteFileNode struct{}

func (n *ReadWriteFileNode) Type() string { return "data.read_write_file" }

// PublishesToThirdParty: data.read_write_file sends nothing off this machine (issue #1418).
func (ReadWriteFileNode) PublishesToThirdParty() bool { return false }

// WritesLocalStore: data.read_write_file puts what it is given where another run can read it back (issue #1418).
func (ReadWriteFileNode) WritesLocalStore() bool { return true }

const readWriteNode = "data.read_write_file"

var readWriteItemFields = []string{"file_selector", "file_name", "mime_type", "file_extension"}

func (n *ReadWriteFileNode) PerItemConfigFields() []string { return readWriteItemFields }

func (n *ReadWriteFileNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	op := cfgString(config, "operation", "read")
	if op != "read" && op != "write" {
		return nil, fmt.Errorf("%s: operation: unknown operation %q (want read or write)", readWriteNode, op)
	}
	engine := workflow.NewExpressionEngine()
	var out []workflow.Item
	for i, item := range inputItemsOrOne(input.Items) {
		cfg, err := resolveItemConfig(engine, config, readWriteItemFields, item, input)
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", readWriteNode, i, err)
		}
		var items []workflow.Item
		if op == "read" {
			items, err = readFiles(ctx, cfg)
		} else {
			items, err = writeFile(ctx, item, cfg)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", readWriteNode, i, err)
		}
		out = append(out, items...)
	}
	return mainOutput(out), nil
}

func readFiles(ctx context.Context, cfg map[string]interface{}) ([]workflow.Item, error) {
	selector := cfgString(cfg, "file_selector", "")
	if selector == "" {
		return nil, fmt.Errorf("file_selector: is required")
	}
	paths, err := globFiles(ctx, selector, cfgBool(cfg, "literal_brackets", true))
	if err != nil {
		return nil, fmt.Errorf("file_selector: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("file_selector: no file matches %q", selector)
	}
	prop := cfgString(cfg, "data_property_name", defaultBinaryProperty)
	items := make([]workflow.Item, 0, len(paths))
	for _, p := range paths {
		data, err := readCapped(p)
		if err != nil {
			return nil, err
		}
		meta := binaryMeta{
			FileName:  cfgString(cfg, "file_name", filepath.Base(p)),
			MimeType:  cfgString(cfg, "mime_type", ""),
			Extension: cfgString(cfg, "file_extension", ""),
		}
		item := newFileItem(prop, data, meta)
		item.JSON["file_path"] = p
		items = append(items, item)
	}
	return items, nil
}

// readCapped reads a whole file, refusing one over maxBinaryBytes.
func readCapped(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if len(data) > maxBinaryBytes {
		return nil, fmt.Errorf("read %s: file is over the %d byte limit", p, maxBinaryBytes)
	}
	return data, nil
}

func writeFile(ctx context.Context, item workflow.Item, cfg map[string]interface{}) ([]workflow.Item, error) {
	name := cfgString(cfg, "file_name", "")
	if name == "" {
		return nil, fmt.Errorf("file_name: is required for operation write (the path to write)")
	}
	path, err := fsconfine.WritePath(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("file_name: %w", err)
	}
	prop := cfgString(cfg, "data_property_name", defaultBinaryProperty)
	data, _, err := readBinary(item, prop)
	if err != nil {
		return nil, fmt.Errorf("data_property_name: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if cfgBool(cfg, "append", false) {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	out := cloneItem(item)
	out.JSON["file_path"] = path
	out.JSON["bytes_written"] = len(data)
	return []workflow.Item{out}, nil
}
