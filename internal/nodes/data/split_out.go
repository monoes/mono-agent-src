package data

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SplitOutNode turns a list inside each item into separate items.
// Type: "data.split_out" (n8n: Split Out, and Item Lists' splitOutItems).
//
// Config:
//
//	field_to_split_out      comma-separated field paths ("$binary" splits binary properties)
//	include                 no_other_fields | all_other_fields | selected_other_fields
//	fields_to_include       comma-separated paths copied when include=selected_other_fields
//	destination_field_name  comma-separated output names, one per split field
//	disable_dot_notation    treat field names literally
//	include_binary          copy the input item's binary data onto every output item
type SplitOutNode struct{}

func (n *SplitOutNode) Type() string { return "data.split_out" }

// PublishesToThirdParty: data.split_out sends nothing off this machine (issue #1418).
func (SplitOutNode) PublishesToThirdParty() bool { return false }

const binarySplitField = "$binary"

func (n *SplitOutNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	fields := cfgNames(config, "field_to_split_out")
	for i, f := range fields {
		fields[i] = strings.TrimPrefix(f, "$json.")
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("data.split_out: field_to_split_out is required")
	}
	dests := cfgNames(config, "destination_field_name")
	if len(dests) > 0 && len(dests) != len(fields) {
		return nil, fmt.Errorf("data.split_out: destination_field_name: give one destination per field to split out (%d fields, %d destinations)", len(fields), len(dests))
	}
	include := normalizeInclude(cfgString(config, "include", "no_other_fields"))
	switch include {
	case "no_other_fields", "all_other_fields", "selected_other_fields":
	default:
		return nil, fmt.Errorf("data.split_out: include: unknown value %q (want no_other_fields, all_other_fields or selected_other_fields)", include)
	}
	toInclude := cfgNames(config, "fields_to_include")
	if include == "selected_other_fields" && len(toInclude) == 0 {
		return nil, fmt.Errorf("data.split_out: fields_to_include is required when include is selected_other_fields")
	}
	literal := cfgBool(config, "disable_dot_notation", false)
	includeBinary := cfgBool(config, "include_binary", false)
	multi := len(fields) > 1

	var out []workflow.Item
	for _, item := range input.Items {
		src := item.JSON
		if src == nil {
			src = map[string]interface{}{}
		}
		var rows []workflow.Item
		ensure := func(i int) {
			for len(rows) <= i {
				rows = append(rows, workflow.Item{JSON: map[string]interface{}{}})
			}
		}

		for fi, field := range fields {
			dest := ""
			if len(dests) > 0 {
				dest = dests[fi]
			}
			if field == binarySplitField {
				props := binaryProperties(item)
				sort.Strings(props)
				for i, p := range props {
					ensure(i)
					data, meta, err := readBinary(item, p)
					if err != nil {
						return nil, fmt.Errorf("data.split_out: %w", err)
					}
					if rows[i].Binary == nil {
						rows[i].Binary = map[string][]byte{}
					}
					rows[i].Binary[p] = data
					rows[i].JSON[p] = item.JSON[p]
					if meta.FileName != "" {
						rows[i].JSON["file_name"] = meta.FileName
					}
				}
				continue
			}

			outName := dest
			if outName == "" {
				outName = field
			}
			for i, el := range splitEntries(src, field, literal) {
				ensure(i)
				obj, isObj := el.(map[string]interface{})
				if isObj && include == "no_other_fields" && dest == "" && !multi {
					for k, v := range obj {
						rows[i].JSON[k] = deepCopyValue(v)
					}
					continue
				}
				setPath(rows[i].JSON, outName, deepCopyValue(el), literal || dest == "")
			}
		}

		for _, row := range rows {
			switch include {
			case "all_other_fields":
				merged := deepCopyMap(src)
				for _, f := range fields {
					unsetPath(merged, f, literal)
				}
				for k, v := range row.JSON {
					merged[k] = v
				}
				row.JSON = merged
			case "selected_other_fields":
				for _, f := range toInclude {
					v, _ := getPath(src, f, literal)
					row.JSON[f] = deepCopyValue(v)
				}
			}
			if includeBinary && row.Binary == nil && item.Binary != nil {
				row.Binary = item.Binary
			}
			out = append(out, row)
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// splitEntries returns the elements to split: a list as-is, an object's
// values (sorted by key for a stable order), a scalar as a one-element list,
// a missing field as nothing.
func splitEntries(src map[string]interface{}, field string, literal bool) []interface{} {
	v, ok := getPath(src, field, literal)
	if !ok || v == nil {
		if ok {
			return []interface{}{nil}
		}
		return nil
	}
	switch t := v.(type) {
	case []interface{}:
		return t
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]interface{}, len(keys))
		for i, k := range keys {
			out[i] = t[k]
		}
		return out
	default:
		return []interface{}{v}
	}
}

// normalizeInclude accepts n8n's camelCase option values as well as ours.
func normalizeInclude(v string) string {
	switch v {
	case "noOtherFields":
		return "no_other_fields"
	case "allOtherFields":
		return "all_other_fields"
	case "selectedOtherFields":
		return "selected_other_fields"
	}
	return v
}
