package apipkg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// installPkg writes a package into a fresh HOME's install dir and reloads.
func installPkg(t *testing.T, files map[string]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for id, js := range files {
		dir := filepath.Join(home, ".monoagent", "apipkgs", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	Reload()
	t.Cleanup(Reload)
}

// fakeNative stands in for a native service node with the 3-line hook wave 2
// adds: unknown ops (or any resource) go to the API package.
type fakeNative struct{}

func (fakeNative) Type() string { return "service.fakenative" }
func (fakeNative) Execute(ctx context.Context, in workflow.NodeInput, cfg map[string]any) ([]workflow.NodeOutput, error) {
	op, _ := cfg["operation"].(string)
	switch {
	case op == "native_ping" && cfg["resource"] == nil:
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{{JSON: map[string]any{"native": true}}}}}, nil
	default:
		if res, _ := cfg["resource"].(string); res != "" || HasExtension("service.fakenative", "", op) {
			return ExecuteExtension(ctx, "service.fakenative", in, cfg)
		}
		return nil, fmt.Errorf("fakenative: unknown operation %q", op)
	}
}

const fakeExt = `{"$schema":"apipkg.v1","id":"fakeext","extends":"service.fakenative","title":"Fake","base_url":"https://fake.test",
  "credentials":[{"platform":"fakeext","methods":["bearer"],"fields":[{"key":"token","label":"Token","required":true}],
    "inject":{"headers":{"Authorization":"Bearer {{ cred.token }}"}}}],
  "resources":[{"value":"gist","label":"Gist"}],
  "operations":[{"resource":"gist","operation":"get","title":"Get a gist","method":"GET","path":"/gists/{{ p.gistId }}",
    "fields":[{"key":"gistId","label":"Gist ID","type":"text","required":true}]}]}`

func TestExtensionRunsFromNativeNode(t *testing.T) {
	installPkg(t, map[string]string{"fakeext": fakeExt})
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("service.fakenative", func() workflow.NodeExecutor { return fakeNative{} })
	Register(reg)
	if reg.Has("app.fakeext") {
		t.Error("an extends package must not register its own node type")
	}
	f, _ := reg.Get("service.fakenative")
	node := f()

	out, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]any{"operation": "native_ping"})
	if err != nil || out[0].Items[0].JSON["native"] != true {
		t.Fatalf("native op: %v %v", out, err)
	}
	srv := newMockServer(t, map[string]any{"id": "g1"}, 200)
	out, err = node.Execute(context.Background(), workflow.NodeInput{}, map[string]any{
		"resource": "gist", "operation": "get", "gistId": "g1", "credential": map[string]any{"token": "T"}})
	if err != nil || out[0].Items[0].JSON["id"] != "g1" {
		t.Fatalf("extension op: %v %v", out, err)
	}
	if r := srv.requests()[0]; r.path != "/gists/g1" || r.header.Get("Authorization") != "Bearer T" {
		t.Errorf("request: %s %v", r.path, r.header)
	}
	if !HasExtension("service.fakenative", "gist", "get") || HasExtension("service.fakenative", "gist", "nope") {
		t.Error("HasExtension")
	}
	if _, err := ExecuteExtension(context.Background(), "service.none", workflow.NodeInput{}, nil); err == nil {
		t.Error("ExecuteExtension without a package should fail")
	}
}

func TestExtendSchemaMergesIntoNative(t *testing.T) {
	installPkg(t, map[string]string{"fakeext": fakeExt})
	p, _ := ByID("fakeext")
	base := `{"credential_platform":"fakenative","fields":[{"key":"operation","label":"Operation","type":"select","options":["native_ping"]}]}`
	out, ok := p.extendSchema([]byte(base))
	if !ok {
		t.Fatal("extendSchema failed")
	}
	var doc struct {
		CredentialPlatform string           `json:"credential_platform"`
		Fields             []map[string]any `json:"fields"`
		ExtendedBy         []string         `json:"extended_by"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, f := range doc.Fields {
		keys = append(keys, f["key"].(string))
	}
	if strings.Join(keys, ",") != "operation,resource,operation,gistId" || doc.CredentialPlatform != "fakenative" ||
		doc.ExtendedBy[0] != "fakeext" {
		t.Errorf("merged schema: %s", out)
	}
	if doc.Fields[1]["default"] != "" || doc.Fields[1]["options"].([]any)[0] != "" {
		t.Errorf("an extension's resource select must default to the native ops: %v", doc.Fields[1])
	}
	// The native operation select shows only while no resource is picked;
	// the resource's own operation select takes over once one is.
	if dep, _ := doc.Fields[0]["depends_on"].(map[string]any); dep == nil || dep["key"] != "resource" ||
		len(dep["values"].([]any)) != 1 || dep["values"].([]any)[0] != "" {
		t.Errorf("native operation select must depend on an empty resource: %v", doc.Fields[0])
	}

	// Without resources, package ops join the native operation select.
	p2 := mustParse(t, `{"$schema":"apipkg.v1","id":"x","extends":"service.fakenative","title":"X","base_url":"https://x.test",
	  "operations":[{"resource":"","operation":"extra_op","title":"Extra","method":"GET","path":"/e"}]}`)
	out, _ = p2.extendSchema([]byte(base))
	if !strings.Contains(string(out), `"options":["native_ping","extra_op"]`) {
		t.Errorf("ops not merged: %s", out)
	}
}

func TestExtensionSchemaThroughWorkflowProvider(t *testing.T) {
	// service.huggingface: n8n has no such node, so no catalog package
	// extends it and the installed fake is the only extender.
	installPkg(t, map[string]string{"ghx": strings.Replace(
		strings.Replace(fakeExt, `"id":"fakeext"`, `"id":"ghx"`, 1), "service.fakenative", "service.huggingface", 1)})
	native, ok := workflow.ReadEmbeddedSchema("service.huggingface")
	if !ok || !strings.Contains(string(native), `"extended_by":["ghx"]`) || !strings.Contains(string(native), `"gistId"`) {
		t.Fatalf("service.huggingface schema not extended: %s", native)
	}
	s, err := workflow.LoadDefaultSchema("service.huggingface")
	if err != nil || s.Fields[0].Key == "gistId" {
		t.Errorf("native fields must stay first: %v", err)
	}
	if b, ok := SchemaFor("service.huggingface"); !ok || strings.Count(string(b), `"gistId"`) != 1 {
		t.Errorf("SchemaFor merges once: %s", b)
	}
}
