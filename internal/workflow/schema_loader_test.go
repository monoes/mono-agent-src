package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLoadDefaultSchema_KnownType(t *testing.T) {
	schema, err := LoadDefaultSchema("service.google_sheets")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(schema.Fields) == 0 {
		t.Fatal("expected fields for service.google_sheets")
	}
	var found bool
	for _, f := range schema.Fields {
		if f.Key == "spreadsheet_id" && f.Type == "resource_picker" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected spreadsheet_id resource_picker field")
	}
}

func TestLoadDefaultSchema_UnknownType(t *testing.T) {
	schema, err := LoadDefaultSchema("unknown.node_type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected non-nil schema for unknown type")
	}
	if len(schema.Fields) != 0 {
		t.Fatalf("expected empty fields for unknown type, got %d", len(schema.Fields))
	}
}

func TestLoadDefaultSchema_BrowserFallback(t *testing.T) {
	cases := []struct {
		nodeType    string
		expectField string
	}{
		{"linkedin.find_by_keyword", "keywords"},
		{"instagram.send_dms", "targets"},
		{"x.engage_with_posts", "keywords"},
		{"tiktok.export_followers", "profileUrl"},
		{"instagram.publish_post", "text"},
		{"linkedin.scrape_profile_info", "targets"},
	}
	for _, tc := range cases {
		schema, err := LoadDefaultSchema(tc.nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.nodeType, err)
		}
		if len(schema.Fields) == 0 {
			t.Fatalf("%s: expected fields, got none", tc.nodeType)
		}
		var found bool
		for _, f := range schema.Fields {
			if f.Key == tc.expectField {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected field %q", tc.nodeType, tc.expectField)
		}
	}
}

func TestListEmbeddedSchemas(t *testing.T) {
	types := ListEmbeddedSchemas()
	if len(types) < 50 {
		t.Fatalf("expected at least 50 embedded schemas, got %d", len(types))
	}
}

// TestLoadDefaultSchema_OutlookAndHumanInLoop is a regression test: these node
// types previously had no schema file, so LoadDefaultSchema silently fell back
// to an empty-fields schema and the canvas config panel rendered no inputs.
func TestLoadDefaultSchema_OutlookAndHumanInLoop(t *testing.T) {
	cases := []struct {
		nodeType    string
		expectField string
	}{
		{"comm.outlook_send", "email"},
		{"comm.outlook_read", "email"},
		{"core.human_in_loop", "editable_fields"},
	}
	for _, tc := range cases {
		schema, err := LoadDefaultSchema(tc.nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.nodeType, err)
		}
		if len(schema.Fields) == 0 {
			t.Fatalf("%s: expected non-empty fields, got none", tc.nodeType)
		}
		var found bool
		for _, f := range schema.Fields {
			if f.Key == tc.expectField {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected field %q", tc.nodeType, tc.expectField)
		}
	}
}

// TestLoadDefaultSchema_AINodes: the six deprecated ai.* node types keep a
// schema so a saved workflow that still has one renders its config, but the
// provider_id field is gone with the AI provider stack it pointed at.
func TestLoadDefaultSchema_AINodes(t *testing.T) {
	types := []string{"ai.chat", "ai.extract", "ai.classify", "ai.transform", "ai.embed", "ai.agent"}
	for _, nodeType := range types {
		schema, err := LoadDefaultSchema(nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", nodeType, err)
		}
		if len(schema.Fields) == 0 {
			t.Fatalf("%s: expected non-empty fields, got none", nodeType)
		}
		for _, f := range schema.Fields {
			if f.Key == "provider_id" {
				t.Errorf("%s: still has a provider_id field", nodeType)
			}
		}
	}
}

func TestNodeSchema_SubNodeKeysRoundTrip(t *testing.T) {
	root := `{"credential_platform":null,"fields":[],"sub_inputs":[{"handle":"ai_languageModel","label":"Chat Model","max":1,"required":true},{"handle":"ai_tool","label":"Tool"}]}`
	var s NodeSchema
	if err := json.Unmarshal([]byte(root), &s); err != nil {
		t.Fatal(err)
	}
	want := []NodeSubInput{{Handle: "ai_languageModel", Label: "Chat Model", Max: 1, Required: true}, {Handle: "ai_tool", Label: "Tool"}}
	if !reflect.DeepEqual(s.SubInputs, want) {
		t.Fatalf("sub_inputs = %+v, want %+v", s.SubInputs, want)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != root {
		t.Fatalf("round trip:\n got %s\nwant %s", out, root)
	}

	sub := `{"credential_platform":null,"fields":[],"sub_output":"ai_tool"}`
	var ss NodeSchema
	if err := json.Unmarshal([]byte(sub), &ss); err != nil {
		t.Fatal(err)
	}
	if ss.SubOutput != "ai_tool" {
		t.Fatalf("sub_output = %q", ss.SubOutput)
	}
	if out, _ := json.Marshal(ss); string(out) != sub {
		t.Fatalf("round trip:\n got %s\nwant %s", out, sub)
	}

	// Schemas without the keys keep their JSON unchanged.
	if out, _ := json.Marshal(NodeSchema{Fields: []NodeSchemaField{}}); strings.Contains(string(out), "sub_") {
		t.Fatalf("empty schema carries sub keys: %s", out)
	}
}
