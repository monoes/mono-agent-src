package schemagen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateSchema_CoreFilter(t *testing.T) {
	schema, err := GenerateSchema("../../nodes/control/filter_schema.go", "FilterNodeSchema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema.CredentialPlatform != nil {
		t.Fatalf("expected nil credential_platform, got %v", *schema.CredentialPlatform)
	}
	if len(schema.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d: %+v", len(schema.Fields), schema.Fields)
	}

	condition := schema.Fields[0]
	if condition.Key != "condition" || condition.Type != "text" || !condition.Required {
		t.Fatalf("unexpected condition field: %+v", condition)
	}
	if condition.Placeholder != "{{ $json.age > 18 }}" {
		t.Fatalf("unexpected placeholder: %q", condition.Placeholder)
	}

	mode := schema.Fields[1]
	if mode.Key != "mode" || mode.Type != "select" {
		t.Fatalf("unexpected mode field: %+v", mode)
	}
	if len(mode.Options) != 2 || mode.Options[0] != "keep" || mode.Options[1] != "remove" {
		t.Fatalf("unexpected mode options: %v", mode.Options)
	}
	if mode.Default != "keep" {
		t.Fatalf("unexpected mode default: %v (%T)", mode.Default, mode.Default)
	}
}

func TestGenerateSchema_HTTPRequest_DependsOnAndNumbers(t *testing.T) {
	schema, err := GenerateSchema("../../nodes/http/request_schema.go", "RequestNodeSchema")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var bodyField, timeoutField, maxBodyField = -1, -1, -1
	for i, f := range schema.Fields {
		switch f.Key {
		case "body":
			bodyField = i
		case "timeout":
			timeoutField = i
		case "max_body_mb":
			maxBodyField = i
		}
	}
	if bodyField == -1 || timeoutField == -1 || maxBodyField == -1 {
		t.Fatalf("missing expected fields in %+v", schema.Fields)
	}

	body2 := schema.Fields[bodyField]
	if body2.DependsOn == nil || body2.DependsOn.Key != "method" {
		t.Fatalf("expected body to depend on method, got %+v", body2.DependsOn)
	}
	if len(body2.DependsOn.Values) != 3 || body2.DependsOn.Values[0] != "POST" {
		t.Fatalf("unexpected depends_on values: %v", body2.DependsOn.Values)
	}

	timeoutFieldVal := schema.Fields[timeoutField]
	if f, ok := timeoutFieldVal.Default.(float64); !ok || f != 30 {
		t.Fatalf("unexpected timeout default: %v (%T)", timeoutFieldVal.Default, timeoutFieldVal.Default)
	}

	maxBody := schema.Fields[maxBodyField]
	if maxBody.Help == "" {
		t.Fatal("expected help text on max_body_mb")
	}
}

func TestGenerateSchema_UnknownStruct(t *testing.T) {
	if _, err := GenerateSchema("../../nodes/control/filter_schema.go", "NoSuchStruct"); err == nil {
		t.Fatal("expected error for unknown struct name")
	}
}

func TestManifest_AllEntriesGenerate(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	for _, entry := range Manifest {
		out, err := RenderJSON(root, entry)
		if err != nil {
			t.Fatalf("%s: %v", entry.NodeType, err)
		}
		if len(out) == 0 {
			t.Fatalf("%s: empty output", entry.NodeType)
		}
	}
}

func TestRenderJSON_KeepsHandWrittenKeys(t *testing.T) {
	root := t.TempDir()
	goDir := filepath.Join(root, "pkg")
	schemaDir := filepath.Join(root, "internal", "workflow", "schemas")
	for _, d := range []string{goDir, schemaDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := "package pkg\n\ntype S struct {\n\tName string `json:\"name\" label:\"Name\"`\n}\n"
	if err := os.WriteFile(filepath.Join(goDir, "s.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := `{"fields":[],"outputs":[{"name":"main"}],"sub_inputs":[{"handle":"ai_languageModel","label":"Chat Model","max":1}],"sub_output":"ai_memory"}`
	if err := os.WriteFile(filepath.Join(schemaDir, "x.node.json"), []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := ManifestEntry{NodeType: "x.node", GoFile: "pkg/s.go", StructName: "S"}
	out, err := RenderJSON(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"outputs", "sub_inputs", "sub_output"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%q dropped on regeneration:\n%s", k, out)
		}
	}
	if m["sub_output"] != "ai_memory" {
		t.Errorf("sub_output = %v", m["sub_output"])
	}
	again, err := RenderJSON(root, entry)
	if err != nil || string(again) != string(out) {
		t.Errorf("rendering is not stable")
	}
}
