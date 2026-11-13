package data

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SummarizeNode aggregates fields across items (count, sum, average, …),
// optionally split into groups — a pivot table over the input.
// Type: "data.summarize" (n8n: Summarize, and Item Lists' summarize).
//
// Config:
//
//	fields_to_summarize     list of {aggregation, field, include_empty, separate_by, custom_separator}
//	                        aggregation: append|average|concatenate|count|count_unique|max|min|sum
//	fields_to_split_by      comma-separated group-by fields
//	output_format           separate_items (one item per group) | single_item (nested object)
//	disable_dot_notation    treat field names literally
//	skip_empty_split_fields drop items whose group-by value is empty
type SummarizeNode struct{}

func (n *SummarizeNode) Type() string { return "data.summarize" }

// PublishesToThirdParty: data.summarize sends nothing off this machine (issue #1418).
func (SummarizeNode) PublishesToThirdParty() bool { return false }

type summaryAgg struct {
	Aggregation     string
	Field           string
	IncludeEmpty    bool
	SeparateBy      string
	CustomSeparator string
}

// summaryPrefixes names each aggregation's output key: <prefix><field>.
var summaryPrefixes = map[string]string{
	"append":       "appended_",
	"average":      "average_",
	"concatenate":  "concatenated_",
	"count":        "count_",
	"count_unique": "unique_count_",
	"max":          "max_",
	"min":          "min_",
	"sum":          "sum_",
}

var summaryAggAliases = map[string]string{"countUnique": "count_unique", "avg": "average"}

func parseSummaryAggs(config map[string]interface{}) ([]summaryAgg, error) {
	list, err := cfgList(config, "fields_to_summarize")
	if err != nil {
		return nil, err
	}
	var aggs []summaryAgg
	for i, raw := range list {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("fields_to_summarize[%d]: expected an object", i)
		}
		a := summaryAgg{
			Aggregation:     cfgString(m, "aggregation", "count"),
			Field:           cfgString(m, "field", ""),
			IncludeEmpty:    cfgBool(m, "include_empty", cfgBool(m, "includeEmpty", false)),
			SeparateBy:      ",",
			CustomSeparator: cfgString(m, "custom_separator", cfgString(m, "customSeparator", "")),
		}
		if alias, ok := summaryAggAliases[a.Aggregation]; ok {
			a.Aggregation = alias
		}
		if _, ok := summaryPrefixes[a.Aggregation]; !ok {
			return nil, fmt.Errorf("fields_to_summarize[%d]: unknown aggregation %q", i, a.Aggregation)
		}
		for _, k := range []string{"separate_by", "separateBy"} {
			if v, ok := m[k].(string); ok {
				a.SeparateBy = v
			}
		}
		if a.Field != "" {
			aggs = append(aggs, a)
		}
	}
	if len(aggs) == 0 {
		return nil, fmt.Errorf("fields_to_summarize: add at least one aggregation with a field")
	}
	return aggs, nil
}

func (n *SummarizeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	aggs, err := parseSummaryAggs(config)
	if err != nil {
		return nil, fmt.Errorf("data.summarize: %w", err)
	}
	format := cfgString(config, "output_format", "separate_items")
	switch format {
	case "separateItems":
		format = "separate_items"
	case "singleItem":
		format = "single_item"
	}
	if format != "separate_items" && format != "single_item" {
		return nil, fmt.Errorf("data.summarize: output_format: unknown value %q (want separate_items or single_item)", format)
	}
	s := summarizer{
		aggs:      aggs,
		literal:   cfgBool(config, "disable_dot_notation", false),
		skipEmpty: cfgBool(config, "skip_empty_split_fields", false),
	}
	rows := make([]map[string]interface{}, 0, len(input.Items))
	for _, it := range input.Items {
		if it.JSON == nil {
			rows = append(rows, map[string]interface{}{})
			continue
		}
		rows = append(rows, it.JSON)
	}
	splits := cfgNames(config, "fields_to_split_by")

	if format == "single_item" {
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: s.nested(rows, splits)}}}}, nil
	}
	var out []workflow.Item
	for _, r := range s.flat(rows, splits) {
		out = append(out, workflow.Item{JSON: r})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

type summarizer struct {
	aggs      []summaryAgg
	literal   bool
	skipEmpty bool
}

func (s summarizer) get(row map[string]interface{}, field string) interface{} {
	v, _ := getPath(row, field, s.literal)
	return v
}

// groups partitions rows by the value of field, in first-seen order.
func (s summarizer) groups(rows []map[string]interface{}, field string) ([]interface{}, map[string][]map[string]interface{}) {
	var keys []interface{}
	byKey := map[string][]map[string]interface{}{}
	for _, r := range rows {
		v := s.get(r, field)
		switch v.(type) {
		case map[string]interface{}, []interface{}:
			b, _ := json.Marshal(v)
			v = string(b)
		}
		if s.skipEmpty {
			if _, isNum := toNumber(v); !isNum && isFalsy(v) {
				continue
			}
		}
		id := fmt.Sprintf("%T:%v", v, v)
		if _, seen := byKey[id]; !seen {
			keys = append(keys, v)
		}
		byKey[id] = append(byKey[id], r)
	}
	return keys, byKey
}

func (s summarizer) flat(rows []map[string]interface{}, splits []string) []map[string]interface{} {
	if len(splits) == 0 {
		return []map[string]interface{}{s.aggregateAll(rows)}
	}
	keys, byKey := s.groups(rows, splits[0])
	var out []map[string]interface{}
	for _, k := range keys {
		for _, r := range s.flat(byKey[fmt.Sprintf("%T:%v", k, k)], splits[1:]) {
			r[normalizeSummaryKey(splits[0])] = k
			out = append(out, r)
		}
	}
	return out
}

func (s summarizer) nested(rows []map[string]interface{}, splits []string) map[string]interface{} {
	if len(splits) == 0 {
		return s.aggregateAll(rows)
	}
	keys, byKey := s.groups(rows, splits[0])
	out := map[string]interface{}{}
	for _, k := range keys {
		out[fmt.Sprintf("%v", k)] = s.nested(byKey[fmt.Sprintf("%T:%v", k, k)], splits[1:])
	}
	return out
}

func (s summarizer) aggregateAll(rows []map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, a := range s.aggs {
		out[normalizeSummaryKey(summaryPrefixes[a.Aggregation]+a.Field)] = s.aggregate(rows, a)
	}
	return out
}

func (s summarizer) aggregate(rows []map[string]interface{}, a summaryAgg) interface{} {
	var vals []interface{}
	for _, r := range rows {
		vals = append(vals, s.get(r, a.Field))
	}
	nonEmpty := func() []interface{} {
		var out []interface{}
		for _, v := range vals {
			if !isEmptyValue(v) {
				out = append(out, v)
			}
		}
		return out
	}
	switch a.Aggregation {
	case "append":
		if !a.IncludeEmpty {
			vals = nonEmpty()
		}
		if vals == nil {
			vals = []interface{}{}
		}
		return vals
	case "concatenate":
		sep := a.SeparateBy
		if sep == "other" {
			sep = a.CustomSeparator
		}
		if !a.IncludeEmpty {
			vals = nonEmpty()
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = summaryText(v)
		}
		return strings.Join(parts, sep)
	case "sum", "average":
		var sum float64
		count := 0
		for _, v := range vals {
			if f, ok := v.(float64); ok {
				sum += f
				count++
			} else if f, ok := toNumber(v); ok {
				if _, isStr := v.(string); !isStr {
					sum += f
					count++
				}
			}
		}
		if a.Aggregation == "sum" {
			return sum
		}
		if count == 0 {
			return nil
		}
		return sum / float64(count)
	case "min", "max":
		var best interface{}
		for _, v := range nonEmpty() {
			if best == nil || (a.Aggregation == "min" && lessValue(v, best)) || (a.Aggregation == "max" && lessValue(best, v)) {
				best = v
			}
		}
		return best
	case "count_unique":
		seen := map[string]bool{}
		for _, v := range vals {
			if !a.IncludeEmpty && isEmptyValue(v) {
				continue
			}
			seen[fmt.Sprintf("%T:%s", v, summaryText(v))] = true
		}
		return len(seen)
	default: // count
		if a.IncludeEmpty {
			return len(vals)
		}
		return len(nonEmpty())
	}
}

var summaryKeyStrip = regexp.MustCompile(`[\]\["]`)

func normalizeSummaryKey(k string) string {
	k = summaryKeyStrip.ReplaceAllString(k, "")
	return strings.NewReplacer(" ", "_", ".", "_").Replace(k)
}

func summaryText(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case map[string]interface{}, []interface{}:
		b, _ := json.Marshal(t)
		return string(b)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}

// toNumber reports numeric values (strings excluded unless parseable).
func toNumber(v interface{}) (float64, bool) {
	if _, isBool := v.(bool); isBool {
		return 0, false
	}
	return toFloat(v)
}

func isFalsy(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	}
	return false
}

// lessValue orders numbers numerically and everything else as text.
func lessValue(a, b interface{}) bool {
	fa, okA := a.(float64)
	fb, okB := b.(float64)
	if !okA {
		fa, okA = toNumber(a)
		if _, s := a.(string); s {
			okA = false
		}
	}
	if !okB {
		fb, okB = toNumber(b)
		if _, s := b.(string); s {
			okB = false
		}
	}
	if okA && okB {
		return fa < fb
	}
	return summaryText(a) < summaryText(b)
}
