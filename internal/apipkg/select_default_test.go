package apipkg

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestRequiredSelectsHaveDefaults: the editor shows a required select
// without a default as "— choose —", an empty pick the user must make
// before anything runs. Every required select of a package schema (and of
// a native schema a package extends) starts on a value.
func TestRequiredSelectsHaveDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Reload()
	t.Cleanup(Reload)
	for _, p := range packages() {
		if len(p.Operations) == 0 {
			continue // trigger-only: no node schema
		}
		b, ok := SchemaFor(p.TargetType())
		if !ok {
			t.Errorf("%s: no schema", p.TargetType())
			continue
		}
		var doc struct {
			Fields []struct {
				Key      string   `json:"key"`
				Type     string   `json:"type"`
				Required bool     `json:"required"`
				Default  any      `json:"default"`
				Options  []string `json:"options"`
				Visible  any      `json:"visible_for"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", p.TargetType(), err)
		}
		for _, f := range doc.Fields {
			if f.Type == "select" && f.Required && len(f.Options) > 0 && !offers(f.Options, f.Default) {
				t.Errorf("%s: required select %q has no default (options %v; for %v)", p.TargetType(), f.Key, f.Options, f.Visible)
			}
		}
	}
}

// offers reports whether v is one of a select's options (or an expression).
func offers(options []string, v any) bool {
	if v == nil {
		return false
	}
	s := fmt.Sprint(v)
	return contains(options, s) || strings.Contains(s, "{{")
}

// A required select left at "" (n8n's habit) starts on its first option;
// an explicit default, an optional select and an expression stay as given.
func TestDefaultRequiredSelects(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"sel","title":"Sel","base_url":"https://sel.test",
	  "operations":[{"resource":"","operation":"go","title":"Go","method":"GET","path":"/",
	    "fields":[
	      {"key":"a","label":"A","type":"select","required":true,"default":"","options":["x","y"]},
	      {"key":"b","label":"B","type":"select","required":true,"options":["x","y"]},
	      {"key":"c","label":"C","type":"select","required":true,"default":"y","options":["x","y"]},
	      {"key":"d","label":"D","type":"select","default":"","options":["x","y"]},
	      {"key":"e","label":"E","type":"select","required":true,"default":"{{ $json.e }}","options":["x","y"]}]}]}`)
	want := map[string]any{"a": "x", "b": "x", "c": "y", "d": "", "e": "{{ $json.e }}"}
	for _, f := range p.Operations[0].Fields {
		if f.Default != want[f.Key] {
			t.Errorf("%s default = %#v, want %#v", f.Key, f.Default, want[f.Key])
		}
	}
}
