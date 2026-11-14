package control

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SortNode sorts items by a dot-path field value.
// Config fields:
//
//	"field" (string, required): dot-path field to sort by, e.g. "name" or "address.city"
//	"order" (string, optional): "asc" (default) or "desc"
//	"type"  (string, optional): "string" (default), "number", or "date"
type SortNode struct{}

func (n *SortNode) Type() string { return "core.sort" }

// PublishesToThirdParty: core.sort sends nothing off this machine (issue #1418).
func (SortNode) PublishesToThirdParty() bool { return false }

func (n *SortNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	if field == "" {
		return nil, fmt.Errorf("sort: 'field' config is required")
	}
	order, _ := config["direction"].(string)
	if order == "" {
		order, _ = config["order"].(string)
	}
	if order == "" {
		order = "asc"
	}
	sortType, _ := config["type"].(string)
	if sortType == "" {
		sortType = "string"
	}

	items := make([]workflow.Item, len(input.Items))
	copy(items, input.Items)

	// Unmarshal each item into a map for comparison — items already carry JSON maps.
	getField := func(item workflow.Item, path string) interface{} {
		return dotGet(item.JSON, path)
	}

	var sortErr error
	sort.SliceStable(items, func(i, j int) bool {
		vi := getField(items[i], field)
		vj := getField(items[j], field)

		// For descending order, compare the operands in reverse rather than
		// negating the result — negating reports i<j and j<i both true for
		// equal keys, violating strict weak ordering and destroying the
		// stability that sort.SliceStable provides for ties.
		if order == "desc" {
			vi, vj = vj, vi
		}

		var less bool
		switch sortType {
		case "number":
			fi, erri := toFloat(vi)
			fj, errj := toFloat(vj)
			if erri != nil || errj != nil {
				// fall back to string compare
				less = fmt.Sprintf("%v", vi) < fmt.Sprintf("%v", vj)
			} else {
				less = fi < fj
			}
		case "date":
			ti, erri := toTime(vi)
			tj, errj := toTime(vj)
			if erri != nil || errj != nil {
				less = fmt.Sprintf("%v", vi) < fmt.Sprintf("%v", vj)
			} else {
				less = ti.Unix() < tj.Unix()
			}
		default: // "string"
			less = fmt.Sprintf("%v", vi) < fmt.Sprintf("%v", vj)
		}

		return less
	})

	if sortErr != nil {
		return nil, sortErr
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: items},
	}, nil
}

// dotGet retrieves a value from a nested map using a dot-separated path.
func dotGet(data map[string]interface{}, path string) interface{} {
	parts := strings.SplitN(path, ".", 2)
	val, ok := data[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		return val
	}
	nested, ok := val.(map[string]interface{})
	if !ok {
		// Try to unmarshal if it's a JSON string
		if s, ok2 := val.(string); ok2 {
			var m map[string]interface{}
			if json.Unmarshal([]byte(s), &m) == nil {
				return dotGet(m, parts[1])
			}
		}
		return nil
	}
	return dotGet(nested, parts[1])
}

// toFloat converts an arbitrary value to float64 via Sprintf + ParseFloat.
func toFloat(v interface{}) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("nil value")
	}
	switch tv := v.(type) {
	case float64:
		return tv, nil
	case float32:
		return float64(tv), nil
	case int:
		return float64(tv), nil
	case int64:
		return float64(tv), nil
	default:
		return strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
	}
}

// toTime parses a value as RFC3339 time.
func toTime(v interface{}) (time.Time, error) {
	if v == nil {
		return time.Time{}, fmt.Errorf("nil value")
	}
	s := fmt.Sprintf("%v", v)
	return time.Parse(time.RFC3339, s)
}
