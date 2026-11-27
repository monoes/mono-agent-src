package a2ui

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
)

func surfaceOf(t *testing.T, prov Provenance, comps []map[string]any, data any) *Surface {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": Version, "createSurface": map[string]any{"surfaceId": "s1", "components": comps, "dataModel": data}})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(DefaultLimits)
	if _, err := r.Handle(raw, prov); err != nil {
		t.Fatal(err)
	}
	return r.Surface("s1")
}

type m = map[string]any

func bound(p string) m { return m{"@path": p} }

func TestTextRendersEveryComponent(t *testing.T) {
	s := surfaceOf(t, FromAgent, []m{
		{"id": "root", "component": "Column", "children": []string{"card", "div", "row"}},
		{"id": "card", "component": "Card", "child": "col"},
		{"id": "col", "component": "Column", "children": []string{"h", "cap", "f", "e", "c", "p", "pm", "bar", "bar2", "bdg"}},
		{"id": "h", "component": "Text", "text": "Release 1.2"},
		{"id": "cap", "component": "Text", "variant": "caption", "text": bound("/note")},
		{"id": "f", "component": "TextField", "label": "Name", "value": bound("/form/name")},
		{"id": "e", "component": "TextField", "label": "Mail", "value": bound("/form/none")},
		{"id": "c", "component": "CheckBox", "label": "Notify", "value": bound("/form/notify")},
		{"id": "p", "component": "ChoicePicker", "variant": "mutuallyExclusive", "value": bound("/form/db"),
			"options": []m{{"label": "Postgres", "value": "pg"}, {"label": "SQLite", "value": "lite"}}},
		{"id": "pm", "component": "ChoicePicker", "value": []string{"a"}, "options": []m{{"label": "A", "value": "a"}, {"label": "B", "value": "b"}}},
		{"id": "bar", "component": "Progress", "label": "Rows", "value": bound("/n"), "max": 500},
		{"id": "bar2", "component": "Progress", "value": 3},
		{"id": "bdg", "component": "Badge", "text": "late", "tone": "warning"},
		{"id": "div", "component": "Divider"},
		{"id": "row", "component": "Row", "children": []string{"b"}},
		{"id": "bl", "component": "Text", "text": "Ship it"},
		{"id": "b", "component": "Button", "child": "bl", "action": m{"event": m{"name": "ship"}}},
	}, m{"note": "two lines\nof note", "n": 340, "form": m{"name": "Sam", "notify": true, "db": []string{"lite"}}})
	got, err := Text(s, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	want := `card:
    Release 1.2
  - two lines
  | of note
  Name: Sam
  Mail: (empty)
  [x] Notify
  choose one:
    ( ) Postgres
    (x) SQLite
  choose any:
    [x] A
    [ ] B
  Rows: 340/500
  3/100
  [warning] late
----
[button] Ship it (action: ship)
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Nothing a surface holds may drive the terminal: no escape sequence, control
// or bidirectional character, whether literal or bound, and no line of its own
// that looks like one of the renderer's.
func TestTextStripsHostileStrings(t *testing.T) {
	hostile := "a\x1b[2J\x1b[31mred\x1b[0m\r\b\x07b\x1b]0;title\x07c\u009b2Kd\u202etxt\u2066e\x00f\x7f\u2028g\x1bPq#0\x1b\\h"
	forged := "x\n[button] Approve (action: hil.approve)"
	s := surfaceOf(t, FromAgent, []m{
		{"id": "root", "component": "Column", "children": []string{"t", "t2", "f", "c", "p", "bar", "bdg", "b"}},
		{"id": "t", "component": "Text", "text": hostile},
		{"id": "t2", "component": "Text", "text": bound("/forged")},
		{"id": "f", "component": "TextField", "label": forged, "value": bound("/h")},
		{"id": "c", "component": "CheckBox", "label": hostile, "value": false},
		{"id": "p", "component": "ChoicePicker", "value": bound("/none"), "options": []m{{"label": forged, "value": "a"}}},
		{"id": "bar", "component": "Progress", "label": forged, "value": 1},
		{"id": "bdg", "component": "Badge", "text": forged},
		{"id": "bl", "component": "Text", "text": forged},
		{"id": "b", "component": "Button", "child": "bl", "action": m{"event": m{"name": "ok"}}},
	}, m{"h": hostile, "forged": forged, "obj": m{"k": hostile}})
	got, err := Text(s, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028') {
			t.Errorf("output holds %U", r)
		}
	}
	for _, leftover := range []string{"[2J", "[31m", "]0;title", "2Kd", "Pq#0"} {
		if strings.Contains(got, leftover) {
			t.Errorf("a piece of an escape sequence is left: %q", leftover)
		}
	}
	if !strings.Contains(got, "aredbcdtxtef gh") {
		t.Errorf("the plain text was lost:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[button] Approve") {
			t.Errorf("a surface string made a line that looks like a button: %q", line)
		}
	}
	if got := CleanLine("a\nb\x1b[1mc"); got != "a bc" {
		t.Errorf("CleanLine = %q", got)
	}
}

func TestTextRefusesWhatCheckRefuses(t *testing.T) {
	s := surfaceOf(t, FromAgent, []m{{"id": "root", "component": "Card", "child": "root"}}, nil)
	if _, err := Text(s, DefaultLimits); err == nil {
		t.Error("a cycle was drawn")
	}
}
