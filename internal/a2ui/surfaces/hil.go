package surfaces

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/monoes/mono-agent/internal/a2ui"
)

// HILItem mirrors the pending Human-in-Loop row `hil_list` returns
// (internal/mcp/tools_nodes_hil.go hilItem), so a caller can unmarshal that
// JSON straight into it.
type HILItem struct {
	ID           string         `json:"id"`
	NodeName     string         `json:"node_name"`
	WorkflowName string         `json:"workflow_name"`
	ReadonlyData map[string]any `json:"readonly_data"`
	EditableData map[string]any `json:"editable_data"`
}

// HILSurfaceID is the surface id of a workflow approval.
func HILSurfaceID(id string) string { return "hil-" + id }

// Layout budget of the HIL surface, from the catalog caps: 80 components and
// 40 children per container. Fixed parts: root, body, title, sub, divider,
// more, actions and two buttons with their labels (4) = 11 components, and
// title, sub, divider, more, actions = 5 body children.
const (
	hilFixedComps    = 11
	hilFixedChildren = 5
)

// HIL builds the approval surface of one pending workflow HIL item.
//
// Read-only fields become a caption plus a Text row each, in key order;
// when they do not all fit the component budget the rest are replaced by one
// "... and N more fields not shown" line, and each value over the text cap
// is cut with Truncated. Editable fields become one TextField each, bound to
// /edit/<key>; they are never cut (see ErrTooLarge). Only strings, numbers
// and booleans are editable, and the key "id" is reserved for the item id.
func HIL(it HILItem) ([][]byte, error) {
	if err := checkID("hil", it.ID); err != nil {
		return nil, err
	}
	keys, vals, err := editableFields(it.EditableData)
	if err != nil {
		return nil, err
	}
	return shrinking(func(textCap int) ([][]byte, error) { return buildHIL(it, keys, vals, textCap) })
}

func editableFields(m map[string]any) ([]string, map[string]string, error) {
	lim := a2ui.DefaultLimits
	if len(m) > lim.MaxOptions-1 { // the action context also carries "id"
		return nil, nil, fmt.Errorf("%w: %d editable fields, at most %d fit an action context", ErrTooLarge, len(m), lim.MaxOptions-1)
	}
	keys := make([]string, 0, len(m))
	vals := make(map[string]string, len(m))
	for k, v := range m {
		if k == "" || k == "id" || len(k) > 90 {
			return nil, nil, fmt.Errorf("surfaces: editable field name %q is not usable (empty, \"id\" or over 90 bytes)", k)
		}
		s, err := scalarText(v)
		if err != nil {
			return nil, nil, fmt.Errorf("surfaces: editable field %q: %w", k, err)
		}
		if vals[k], err = editable("editable field "+strconv.Quote(k), s); err != nil {
			return nil, nil, err
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, vals, nil
}

// scalarText renders a string, number or boolean as the text a TextField holds.
func scalarText(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case json.Number:
		return t.String(), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	}
	return "", fmt.Errorf("a %T value cannot be edited as text", v)
}

// displayText renders any read-only value as text.
func displayText(v any) string {
	if s, err := scalarText(v); err == nil {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func buildHIL(it HILItem, keys []string, vals map[string]string, textCap int) ([][]byte, error) {
	lim := a2ui.DefaultLimits
	e := len(keys)
	rows := min((lim.MaxComponents-hilFixedComps-e)/2, (lim.MaxChildren-hilFixedChildren-e)/2)
	if rows < 0 {
		rows = 0
	}
	roKeys := make([]string, 0, len(it.ReadonlyData))
	for k := range it.ReadonlyData {
		roKeys = append(roKeys, k)
	}
	sort.Strings(roKeys)
	hidden := 0
	if len(roKeys) > rows {
		hidden = len(roKeys) - rows
		roKeys = roKeys[:rows]
	}

	b := newBuilder()
	body := []string{"title", "sub", "div"}
	b.text("title", elide(firstNonEmpty(it.NodeName, "Approval needed"), 200), "body")
	b.text("sub", elide("Workflow: "+firstNonEmpty(it.WorkflowName, "(unnamed)"), 200), "caption")
	b.add("div", "Divider", nil)
	for i, k := range roKeys {
		b.text(fmt.Sprintf("ro-%d-k", i), elide(k, 100), "caption")
		b.text(fmt.Sprintf("ro-%d-v", i), elide(displayText(it.ReadonlyData[k]), textCap), "body")
		body = append(body, fmt.Sprintf("ro-%d-k", i), fmt.Sprintf("ro-%d-v", i))
	}
	if hidden > 0 {
		b.text("more", fmt.Sprintf("… and %d more fields not shown", hidden), "caption")
		body = append(body, "more")
	}
	approveCtx := map[string]any{"id": it.ID}
	edit := map[string]any{}
	for i, k := range keys {
		id := fmt.Sprintf("ef-%d", i)
		path := "/edit/" + pointerKey(k)
		b.add(id, "TextField", map[string]any{"label": elide(k, 100), "value": bind(path), "variant": fieldVariant(vals[k])})
		edit[k] = vals[k]
		approveCtx[k] = bind(path)
		body = append(body, id)
	}
	b.data["edit"] = edit
	b.button(btnApprove, "Approve", "primary", "hil.approve", approveCtx)
	b.button(btnReject, "Reject", "danger", "hil.reject", map[string]any{"id": it.ID})
	b.row("actions", btnApprove, btnReject)
	body = append(body, "actions")
	b.column("body", body...)
	b.add("root", "Card", map[string]any{"child": "body"})
	return b.create(HILSurfaceID(it.ID))
}

func fieldVariant(s string) string {
	if len(s) > 120 {
		return "longText"
	}
	return "shortText"
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
