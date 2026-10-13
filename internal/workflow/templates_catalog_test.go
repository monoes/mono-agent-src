package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// universalConfigKeys are read by the engine for every node type, so no
// node schema lists them.
var universalConfigKeys = map[string]bool{"on_error": true, "retry_policy": true}

func TestListTemplates_HelloFirst(t *testing.T) {
	list := ListTemplates()
	if len(list) == 0 || list[0].ID != "hello" {
		t.Fatalf("first template = %v, want hello", list)
	}
}

func TestListTemplates_OrderedFirstThenByName(t *testing.T) {
	list := ListTemplates()
	var unordered []string
	for i, tmpl := range list {
		order := templateMetas[tmpl.ID].Order
		if order > 0 {
			if len(unordered) > 0 {
				t.Errorf("%s (order %d) listed after unordered %v", tmpl.ID, order, unordered)
			}
			if i > 0 {
				if prev := templateMetas[list[i-1].ID].Order; prev > order {
					t.Errorf("%s (order %d) listed after order %d", tmpl.ID, order, prev)
				}
			}
			continue
		}
		unordered = append(unordered, tmpl.Name)
	}
	if !sort.StringsAreSorted(unordered) {
		t.Errorf("unordered templates not sorted by name: %v", unordered)
	}
}

func TestListTemplates_CarriesSummary(t *testing.T) {
	for _, tmpl := range ListTemplates() {
		if tmpl.Summary != templateMetas[tmpl.ID].Summary || tmpl.Summary == "" {
			t.Errorf("%s: summary %q not carried from the template file", tmpl.ID, tmpl.Summary)
		}
	}
}

func TestBundledTemplates_Count(t *testing.T) {
	if n := len(templateFiles); n < 15 || n > 20 {
		t.Errorf("bundled templates = %d, want 15-20", n)
	}
}

// TestBundledTemplates_WellFormed checks every embedded template the way a
// user would hit it: it parses, says what it is, wires existing nodes, and
// configures each node only with keys and values its schema knows.
func TestBundledTemplates_WellFormed(t *testing.T) {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		t.Run(id, func(t *testing.T) {
			data, err := templateFS.ReadFile("templates/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			var wf WorkflowFile
			if err := json.Unmarshal(data, &wf); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, ok := templateFiles[id]; !ok {
				t.Fatalf("template not loaded")
			}
			meta := templateMetas[id]
			if wf.Name == "" || wf.Description == "" || meta.Summary == "" {
				t.Errorf("name/description/summary must all be set")
			}
			for _, cli := range []string{"--input", "on_error", "monoagentcli", "`", "workflow run", "templates run"} {
				if strings.Contains(meta.Summary, cli) {
					t.Errorf("summary is for app users and must not mention %q: %q", cli, meta.Summary)
				}
			}
			if wf.IsActive {
				t.Errorf("templates must start inactive")
			}
			checkTemplateGraph(t, wf)
			for _, n := range wf.Nodes {
				checkTemplateNode(t, n)
			}
		})
	}
}

func checkTemplateGraph(t *testing.T, wf WorkflowFile) {
	t.Helper()
	ids := map[string]bool{}
	triggers := 0
	for _, n := range wf.Nodes {
		if n.ID == "" || ids[n.ID] {
			t.Errorf("node id %q empty or duplicated", n.ID)
		}
		ids[n.ID] = true
		if strings.HasPrefix(n.Type, "trigger.") {
			triggers++
		}
	}
	if triggers != 1 {
		t.Errorf("want exactly one trigger node, got %d", triggers)
	}
	for _, c := range wf.Connections {
		if !ids[c.Source] || !ids[c.Target] {
			t.Errorf("connection %q references a missing node (%s → %s)", c.ID, c.Source, c.Target)
		}
		if c.SourceHandle == "" {
			t.Errorf("connection %q has no source_handle", c.ID)
		}
	}
}

func checkTemplateNode(t *testing.T, n WorkflowFileNode) {
	t.Helper()
	if hint, ok := IsDeprecatedNodeType(n.Type); ok {
		t.Errorf("node %s: deprecated type %s — %s", n.ID, n.Type, hint)
	}
	if _, ok := ReadEmbeddedSchema(n.Type); !ok {
		t.Errorf("node %s: no schema for type %q", n.ID, n.Type)
		return
	}
	schema, err := LoadDefaultSchema(n.Type)
	if err != nil {
		t.Fatalf("node %s: %v", n.ID, err)
	}
	fields := map[string]NodeSchemaField{}
	for _, f := range schema.Fields {
		fields[f.Key] = f
	}
	for key, val := range n.Config {
		f, known := fields[key]
		if !known && !universalConfigKeys[key] {
			t.Errorf("node %s (%s): config key %q is not in the node's schema", n.ID, n.Type, key)
			continue
		}
		if s, ok := val.(string); ok && len(f.Options) > 0 && !strings.Contains(s, "{{") && !containsString(f.Options, s) {
			t.Errorf("node %s (%s): %s = %q, want one of %v", n.ID, n.Type, key, s, f.Options)
		}
	}
	for _, f := range schema.Fields {
		if _, set := n.Config[f.Key]; f.Required && !set && f.DependsOn == nil {
			t.Errorf("node %s (%s): required config key %q missing", n.ID, n.Type, f.Key)
		}
	}
	engine := NewExpressionEngine()
	for _, s := range templateConfigStrings(n.Config) {
		if !strings.Contains(s, "{{") {
			continue
		}
		if _, err := engine.parse(s); err != nil {
			t.Errorf("node %s: expression does not parse: %v", n.ID, err)
		}
	}
}

func templateConfigStrings(v interface{}) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case map[string]interface{}:
		var out []string
		for _, e := range x {
			out = append(out, templateConfigStrings(e)...)
		}
		return out
	case []interface{}:
		var out []string
		for _, e := range x {
			out = append(out, templateConfigStrings(e)...)
		}
		return out
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestBundledTemplates_MatchExamples keeps the bundled copies of the
// examples/ workflows (which the docs link to) from drifting apart.
func TestBundledTemplates_MatchExamples(t *testing.T) {
	pairs := map[string]string{
		"data-pipeline.json":           "data_pipeline",
		"github-issues-to-linear.json": "github_issues_to_linear",
		"http-to-slack.json":           "http_to_slack",
		"morning-briefing.json":        "morning_briefing",
		"sheets-to-gmail.json":         "sheets_to_gmail",
		"stripe-events-to-sheets.json": "stripe_events_to_sheets",
		"telegram-notify.json":         "telegram_notify",
		"webhook-to-postgres.json":     "webhook_to_postgres",
	}
	for file, id := range pairs {
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", file))
		if err != nil {
			t.Fatal(err)
		}
		var example WorkflowFile
		if err := json.Unmarshal(data, &example); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		tmpl, ok := GetTemplate(id)
		if !ok {
			t.Fatalf("template %q missing", id)
		}
		for _, part := range []struct {
			name      string
			got, want interface{}
		}{
			{"name", tmpl.Name, example.Name},
			{"description", tmpl.Description, example.Description},
			{"nodes", tmpl.Nodes, example.Nodes},
			{"connections", tmpl.Connections, example.Connections},
		} {
			if !reflect.DeepEqual(part.got, part.want) {
				t.Errorf("%s: template %s %s differs from the example", file, id, part.name)
			}
		}
	}
}

func TestTemplateInputs_OnlyTriggerData(t *testing.T) {
	wf := WorkflowFile{
		Nodes: []WorkflowFileNode{
			{ID: "t", Name: "Trigger", Type: "trigger.manual"},
			{ID: "a", Type: "http.request", Config: map[string]interface{}{"url": "{{ $json.url }}"}},
			{ID: "b", Type: "core.set", Config: map[string]interface{}{
				"x": "{{ $json.body }}",
				"y": `{{ $node["Trigger"].json.topic }}`,
				"z": `{{ $node["a"].json.status }}`,
			}},
		},
		Connections: []WorkflowFileEdge{
			{Source: "t", SourceHandle: "main", Target: "a"},
			{Source: "a", SourceHandle: "main", Target: "b"},
		},
	}
	got := templateInputs(wf)
	want := []string{"topic", "url"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("inputs = %v, want %v", got, want)
	}
}
