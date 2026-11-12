package data

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ExtractFromFileNode reads a file carried on each item and turns its
// content into JSON (n8n's "Extract from File"): spreadsheet formats become
// one item per row; text, JSON, XML, calendar and base64 land in one field;
// PDFs become their text.
// Type: "data.extract_from_file"
type ExtractFromFileNode struct{}

func (n *ExtractFromFileNode) Type() string { return "data.extract_from_file" }

// PublishesToThirdParty: data.extract_from_file sends nothing off this machine (issue #1418).
func (ExtractFromFileNode) PublishesToThirdParty() bool { return false }

const extractNode = "data.extract_from_file"

func (n *ExtractFromFileNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	op := cfgString(config, "operation", "csv")
	switch op {
	case "csv", "html", "ods", "rtf", "xlsx", "json", "text", "xml", "ics", "binary_to_property", "pdf":
	case "xls":
		return nil, fmt.Errorf("%s: operation xls (legacy Excel 97-2003 .xls) is not supported; save the file as .xlsx and use operation xlsx", extractNode)
	default:
		return nil, fmt.Errorf("%s: operation: unknown operation %q (want csv, html, ics, json, ods, pdf, rtf, text, xml, xlsx or binary_to_property)", extractNode, op)
	}
	prop := cfgString(config, "binary_property_name", defaultBinaryProperty)
	var out []workflow.Item
	for i, item := range input.Items {
		data, _, err := readBinary(item, prop)
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: binary_property_name: %w", extractNode, i, err)
		}
		if len(data) > maxBinaryBytes {
			return nil, fmt.Errorf("%s: item %d: file is %d bytes, over the %d byte limit", extractNode, i, len(data), maxBinaryBytes)
		}
		var items []workflow.Item
		switch op {
		case "pdf":
			items, err = extractPDFItem(item, prop, data, config)
		case "json", "text", "xml", "ics", "binary_to_property":
			items, err = extractToField(op, item, prop, data, config)
		default:
			items, err = extractTable(op, data, config)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", extractNode, i, err)
		}
		out = append(out, items...)
	}
	return mainOutput(out), nil
}

// sourceBase builds the item an extracted value is added to, per
// keep_source: "json" keeps the input's JSON without the file, "binary"
// keeps only the files the item carries, "both" keeps everything.
func sourceBase(item workflow.Item, prop, keep string) workflow.Item {
	switch keep {
	case "both":
		return cloneItem(item)
	case "binary":
		out := workflow.Item{JSON: map[string]interface{}{}, Binary: copyBinaryMap(item.Binary)}
		props := binaryProperties(item)
		if list, isList := item.JSON["files"].([]interface{}); isList {
			var files []interface{}
			for _, f := range list {
				files = append(files, deepCopyValue(f))
			}
			out.JSON["files"] = files
		} else {
			for _, k := range []string{"file_name", "mime_type", "file_extension", "file_size"} {
				if v, ok := item.JSON[k]; ok {
					out.JSON[k] = v
				}
			}
		}
		for _, p := range append(props, prop) {
			if v, ok := getPath(item.JSON, p, false); ok {
				setPath(out.JSON, p, deepCopyValue(v), false)
			}
		}
		return out
	default:
		out := cloneItem(item)
		dropBinary(&out, prop)
		return out
	}
}

// extractToField decodes the file into one value stored at destination_key.
func extractToField(op string, item workflow.Item, prop string, data []byte, config map[string]interface{}) ([]workflow.Item, error) {
	keep, err := keepSourceMode(config)
	if err != nil {
		return nil, err
	}
	dest := cfgString(config, "destination_key", "data")
	if keep != "json" {
		for _, p := range append(binaryProperties(item), prop) {
			if p == dest {
				return nil, fmt.Errorf("destination_key: %q is a file property that keep_source=%s keeps; choose another destination_key", dest, keep)
			}
		}
	}
	var value interface{}
	if op == "binary_to_property" {
		value = base64.StdEncoding.EncodeToString(data)
	} else {
		text, err := decodeText(data, cfgString(config, "encoding", ""), cfgBool(config, "strip_bom", true))
		if err != nil {
			return nil, fmt.Errorf("encoding: %w", err)
		}
		switch op {
		case "json":
			if strings.TrimSpace(text) == "" {
				value = map[string]interface{}{}
			} else if err := json.Unmarshal([]byte(text), &value); err != nil {
				return nil, fmt.Errorf("the file in %q is not valid JSON: %w", prop, err)
			}
		case "ics":
			if value, err = parseICS(text); err != nil {
				return nil, fmt.Errorf("the file in %q: %w", prop, err)
			}
		default:
			value = text
		}
	}
	out := sourceBase(item, prop, keep)
	setPath(out.JSON, dest, value, false)
	return []workflow.Item{out}, nil
}
