package a2ui

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Text renders a live surface as plain text for a terminal: its text lines, the
// current data-model values of its inputs, and its buttons with their action
// names. It refuses a surface that Check refuses. Every string of the surface
// and of its data model goes through CleanLine or cleanLines, so nothing a
// surface holds can move the cursor, recolor or reorder the terminal's text, or
// start a line that looks like one of the renderer's own.
func Text(s *Surface, lim Limits) (string, error) {
	if err := s.Check(lim); err != nil {
		return "", err
	}
	r := &textRenderer{s: s}
	r.draw("root", 0)
	return r.b.String(), nil
}

// ansiSequence matches terminal escape sequences, in their 7-bit (ESC) and
// 8-bit (CSI U+009B) forms.
var ansiSequence = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[PX^_][^\x1b]*(?:\x1b\\\\)?|[ -/]*[0-~])|\u009b[0-?]*[ -/]*[@-~]")

// stripControls removes escape sequences, then every control and format
// character (which covers the bidirectional overrides) except newline; tabs
// and the Unicode line separators become spaces.
func stripControls(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	s = ansiSequence.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t' || r == '\u2028' || r == '\u2029':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
}

// CleanLine is s made safe to print on one line of a terminal.
func CleanLine(s string) string {
	return strings.ReplaceAll(stripControls(s), "\n", " ")
}

type textRenderer struct {
	s *Surface
	b strings.Builder
}

// line writes one entry at a depth; further lines of the same entry are marked
// with "| " so a surface's own text cannot pass for another entry.
func (r *textRenderer) line(depth int, s string) {
	pad := strings.Repeat("  ", depth)
	for i, l := range strings.Split(stripControls(s), "\n") {
		if i > 0 {
			l = "| " + l
		}
		r.b.WriteString(pad + l + "\n")
	}
}

// draw renders a component of a checked surface, whose components form a tree
// within the depth cap.
func (r *textRenderer) draw(id string, depth int) {
	c := r.s.Components[id]
	switch c["component"] {
	case "Column", "Row":
		for _, ref := range childRefs(c) {
			r.draw(ref, depth)
		}
	case "Card":
		r.line(depth, "card:")
		for _, ref := range childRefs(c) {
			r.draw(ref, depth+1)
		}
	case "Text":
		if c["variant"] == "caption" {
			r.line(depth, "- "+r.str(c["text"]))
		} else {
			r.line(depth, "  "+r.str(c["text"]))
		}
	case "Divider":
		r.line(depth, "----")
	case "Button":
		a, _ := c["action"].(map[string]any)
		ev, _ := a["event"].(map[string]any)
		name, _ := ev["name"].(string)
		child, _ := c["child"].(string)
		r.line(depth, fmt.Sprintf("[button] %s (action: %s)", CleanLine(r.label(child)), CleanLine(name)))
	case "TextField":
		v := r.str(c["value"])
		if v == "" {
			v = "(empty)"
		}
		r.line(depth, CleanLine(r.str(c["label"]))+": "+v)
	case "CheckBox":
		mark := "[ ]"
		if b, _ := r.value(c["value"]).(bool); b {
			mark = "[x]"
		}
		r.line(depth, mark+" "+CleanLine(r.str(c["label"])))
	case "ChoicePicker":
		r.choices(c, depth)
	case "Progress":
		r.progress(c, depth)
	case "Badge":
		tone, _ := c["tone"].(string)
		if tone == "" {
			tone = "neutral"
		}
		r.line(depth, "["+tone+"] "+CleanLine(r.str(c["text"])))
	}
}

// label is the text of a button's child, "" when it holds no text of its own.
func (r *textRenderer) label(id string) string {
	c := r.s.Components[id]
	if v, ok := c["text"]; ok {
		return r.str(v)
	}
	var parts []string
	for _, ref := range childRefs(c) {
		if l := r.label(ref); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

func (r *textRenderer) choices(c map[string]any, depth int) {
	chosen := map[string]bool{}
	switch v := r.value(c["value"]).(type) {
	case []any:
		for _, e := range v {
			chosen[formatValue(e)] = true
		}
	case string:
		chosen[v] = true
	}
	open, shut, head := "[", "]", "choose any:"
	if c["variant"] == "mutuallyExclusive" {
		open, shut, head = "(", ")", "choose one:"
	}
	r.line(depth, head)
	opts, _ := c["options"].([]any)
	for _, o := range opts {
		m, _ := o.(map[string]any)
		value, _ := m["value"].(string)
		mark := " "
		if chosen[value] {
			mark = "x"
		}
		r.line(depth+1, open+mark+shut+" "+CleanLine(r.str(m["label"])))
	}
}

// progress prints "n/max"; like the desktop renderer, a missing or non-positive max is 100.
func (r *textRenderer) progress(c map[string]any, depth int) {
	n, ok := number(r.value(c["value"]))
	if !ok {
		n = 0
	}
	max, ok := number(r.value(c["max"]))
	if !ok || max <= 0 {
		max = 100
	}
	s := strconv.FormatFloat(n, 'f', -1, 64) + "/" + strconv.FormatFloat(max, 'f', -1, 64)
	if l := CleanLine(r.str(c["label"])); l != "" {
		s = l + ": " + s
	}
	r.line(depth, s)
}

// value resolves a literal or a {"@path": ...} binding against the data model.
func (r *textRenderer) value(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	path, ok := m["@path"].(string)
	if !ok {
		return v
	}
	cur := r.s.Data
	if path == "" || path == "/" {
		return cur
	}
	for _, p := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		switch t := cur.(type) {
		case map[string]any:
			cur = t[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			cur = t[i]
		default:
			return nil
		}
	}
	return cur
}

func (r *textRenderer) str(v any) string { return formatValue(r.value(v)) }

func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = formatValue(e)
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}

func number(v any) (float64, bool) {
	var f float64
	switch t := v.(type) {
	case json.Number:
		n, err := t.Float64()
		if err != nil {
			return 0, false
		}
		f = n
	case float64:
		f = t
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}
