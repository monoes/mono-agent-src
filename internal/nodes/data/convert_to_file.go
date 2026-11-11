package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ConvertToFileNode turns item JSON into a file (n8n's "Convert to File"):
// all items into one CSV/HTML/ODS/RTF/XLSX/JSON file, or one file per item
// for text, JSON (mode "each"), base64 strings and calendar events.
// Type: "data.convert_to_file"
type ConvertToFileNode struct{}

func (n *ConvertToFileNode) Type() string { return "data.convert_to_file" }

// PublishesToThirdParty: data.convert_to_file sends nothing off this machine (issue #1418).
func (ConvertToFileNode) PublishesToThirdParty() bool { return false }

// PerItemConfigFields holds back the calendar fields (operation "ics") and
// file_name, which per-item operations name from the item.
func (n *ConvertToFileNode) PerItemConfigFields() []string { return icalItemFields }

const convertNode = "data.convert_to_file"

// spreadsheetMime is the media type of each table format.
var spreadsheetMime = map[string]string{
	"csv":  "text/csv",
	"html": "text/html",
	"rtf":  "application/rtf",
	"ods":  odsMime,
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

func (n *ConvertToFileNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	op := cfgString(config, "operation", "csv")
	var (
		items []workflow.Item
		err   error
	)
	switch op {
	case "csv", "html", "rtf", "ods", "xlsx":
		items, err = convertTable(op, input, config)
	case "xls":
		err = fmt.Errorf("operation xls (legacy Excel 97-2003 .xls) is not supported; use xlsx, which every current spreadsheet app opens")
	case "json":
		items, err = convertJSON(input, config)
	case "text", "to_binary":
		items, err = convertPerItem(op, input, config)
	case "ics":
		return wrapItems(icalItems(convertNode, input, config))
	default:
		err = fmt.Errorf("operation: unknown operation %q (want csv, html, ics, json, ods, rtf, text, xlsx or to_binary)", op)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", convertNode, err)
	}
	return mainOutput(items), nil
}

func wrapItems(items []workflow.Item, err error) ([]workflow.NodeOutput, error) {
	if err != nil {
		return nil, err
	}
	return mainOutput(items), nil
}

// batchConfig resolves the held-back fields against the first item, for
// operations that write one file for the whole batch.
func batchConfig(input workflow.NodeInput, config map[string]interface{}) (map[string]interface{}, error) {
	return resolveItemConfig(workflow.NewExpressionEngine(), config, icalItemFields, inputItemsOrOne(input.Items)[0], input)
}

func convertTable(op string, input workflow.NodeInput, config map[string]interface{}) ([]workflow.Item, error) {
	cfg, err := batchConfig(input, config)
	if err != nil {
		return nil, err
	}
	headerRow := cfgBool(cfg, "header_row", true)
	sheet := cfgString(cfg, "sheet_name", "Sheet")
	compress := cfgBool(cfg, "compression", false)
	cols, rows := itemsTable(input.Items)
	grid := tableToRows(cols, rows, headerRow)

	var data []byte
	switch op {
	case "csv":
		delim, err := csvDelimiter(cfgString(cfg, "delimiter", ","))
		if err != nil {
			return nil, err
		}
		if data, err = writeCSVTable(grid, delim); err != nil {
			return nil, fmt.Errorf("csv: %w", err)
		}
	case "html":
		data = writeHTMLTable(grid, headerRow, sheet)
	case "rtf":
		data = writeRTFTable(grid, headerRow)
	case "ods":
		if data, err = writeODSTable(grid, sheet, compress); err != nil {
			return nil, fmt.Errorf("ods: %w", err)
		}
	case "xlsx":
		if data, err = writeXLSXTable(grid, sheet, compress); err != nil {
			return nil, fmt.Errorf("xlsx: %w", err)
		}
	}
	meta := binaryMeta{FileName: cfgString(cfg, "file_name", "File."+op), MimeType: spreadsheetMime[op], Extension: op}
	prop := cfgString(cfg, "binary_property_name", defaultBinaryProperty)
	return []workflow.Item{newFileItem(prop, data, meta)}, nil
}

// marshalJSON renders v as JSON without HTML escaping, optionally indented.
func marshalJSON(v interface{}, format bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if format {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func convertJSON(input workflow.NodeInput, config map[string]interface{}) ([]workflow.Item, error) {
	mode := cfgString(config, "mode", "once")
	if mode != "once" && mode != "each" {
		return nil, fmt.Errorf("mode: %q is not one of once, each", mode)
	}
	engine := workflow.NewExpressionEngine()
	build := func(v interface{}, cfg map[string]interface{}) (workflow.Item, error) {
		raw, err := marshalJSON(v, cfgBool(cfg, "format", false))
		if err != nil {
			return workflow.Item{}, err
		}
		data, err := encodeText(string(raw), cfgString(cfg, "encoding", "utf8"), cfgBool(cfg, "add_bom", false))
		if err != nil {
			return workflow.Item{}, fmt.Errorf("encoding: %w", err)
		}
		meta := binaryMeta{FileName: cfgString(cfg, "file_name", "file.json"), MimeType: "application/json", Extension: "json"}
		return newFileItem(cfgString(cfg, "binary_property_name", defaultBinaryProperty), data, meta), nil
	}
	if mode == "once" {
		cfg, err := batchConfig(input, config)
		if err != nil {
			return nil, err
		}
		all := make([]interface{}, len(input.Items))
		for i, it := range input.Items {
			all[i] = it.JSON
		}
		item, err := build(all, cfg)
		return []workflow.Item{item}, err
	}
	out := make([]workflow.Item, 0, len(input.Items))
	for i, it := range input.Items {
		cfg, err := resolveItemConfig(engine, config, icalItemFields, it, input)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		item, err := build(it.JSON, cfg)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		out = append(out, item)
	}
	return out, nil
}

// convertPerItem handles "text" (a field's text to a file) and "to_binary"
// (a field's base64 string to a file).
func convertPerItem(op string, input workflow.NodeInput, config map[string]interface{}) ([]workflow.Item, error) {
	source := cfgString(config, "source_property", "")
	if source == "" {
		return nil, fmt.Errorf("source_property: is required for operation %s", op)
	}
	engine := workflow.NewExpressionEngine()
	out := make([]workflow.Item, 0, len(input.Items))
	for i, it := range input.Items {
		cfg, err := resolveItemConfig(engine, config, icalItemFields, it, input)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		v, ok := getPath(it.JSON, source, false)
		if !ok || v == nil {
			return nil, fmt.Errorf("item %d: source_property: %q is not set", i, source)
		}
		var data []byte
		meta := binaryMeta{FileName: cfgString(cfg, "file_name", ""), MimeType: cfgString(cfg, "mime_type", "")}
		if op == "to_binary" && cfgBool(cfg, "data_is_base64", true) {
			s, isStr := v.(string)
			if !isStr {
				return nil, fmt.Errorf("item %d: source_property: %q holds %T, want a base64 string", i, source, v)
			}
			if data, err = decodeBase64Loose(s); err != nil {
				return nil, fmt.Errorf("item %d: source_property: %q is not base64: %w", i, source, err)
			}
		} else {
			text, isStr := v.(string)
			if !isStr {
				raw, err := marshalJSON(v, false)
				if err != nil {
					return nil, fmt.Errorf("item %d: %w", i, err)
				}
				text = string(raw)
				if meta.MimeType == "" {
					meta.MimeType = "application/json"
				}
			} else if op == "text" && meta.MimeType == "" {
				meta.MimeType = "text/plain"
			}
			if data, err = encodeText(text, cfgString(cfg, "encoding", "utf8"), cfgBool(cfg, "add_bom", false)); err != nil {
				return nil, fmt.Errorf("encoding: %w", err)
			}
		}
		if len(data) > maxBinaryBytes {
			return nil, fmt.Errorf("item %d: file is %d bytes, over the %d byte limit", i, len(data), maxBinaryBytes)
		}
		if meta.FileName == "" {
			// The platform's mime table may list several extensions for
			// these types; pick the usual one.
			switch meta.MimeType {
			case "text/plain":
				meta.Extension = "txt"
			case "application/json":
				meta.Extension = "json"
			}
		}
		meta = completeMeta(meta, data)
		if meta.FileName == "" {
			meta.FileName = "file"
			if meta.Extension != "" {
				meta.FileName += "." + meta.Extension
			}
		}
		out = append(out, newFileItem(cfgString(cfg, "binary_property_name", defaultBinaryProperty), data, meta))
	}
	return out, nil
}
