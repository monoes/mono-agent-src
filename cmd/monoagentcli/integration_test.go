package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
)

func runIntegration(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	cmd := newIntegrationCmd(&globalConfig{JSONOutput: jsonOut})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String() + errOut.String(), err
}

func integrationHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
	return home
}

func TestIntegrationListAndShow(t *testing.T) {
	integrationHome(t)
	out, err := runIntegration(t, false, "list")
	if err != nil || !strings.Contains(out, "todoist") || !strings.Contains(out, "app.typeform") || !strings.Contains(out, "n8n-nodes-base.todoist") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = runIntegration(t, true, "list")
	var rows []integrationSummary
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) < 3 {
		t.Fatalf("list --json: %v %s", err, out)
	}
	out, err = runIntegration(t, false, "show", "todoist")
	if err != nil || !strings.Contains(out, "POST /tasks") || !strings.Contains(out, "content*") || !strings.Contains(out, "connect todoist") {
		t.Errorf("show: %v\n%s", err, out)
	}
	out, err = runIntegration(t, false, "show", "typeform")
	if err != nil || !strings.Contains(out, "n8n trigger: n8n-nodes-base.typeformTrigger → trigger.webhook") {
		t.Errorf("show typeform: %v\n%s", err, out)
	}
	out, err = runIntegration(t, false, "show", "gmail_more")
	if err != nil || !strings.Contains(out, "n8n trigger: n8n-nodes-base.gmailTrigger → trigger.schedule polling") {
		t.Errorf("show gmail_more: %v\n%s", err, out)
	}
	if _, err := runIntegration(t, false, "show", "nope"); exitCodeFor(err) != 2 {
		t.Errorf("show unknown: exit %d (%v)", exitCodeFor(err), err)
	}
}

func TestIntegrationValidate(t *testing.T) {
	integrationHome(t)
	out, err := runIntegration(t, false, "validate", "../../internal/apipkg/catalog/calendly.json")
	if err != nil || !strings.Contains(out, "valid (calendly, 6 operations)") {
		t.Errorf("valid file: %v %s", err, out)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"$schema":"apipkg.v1","id":"bad","title":"Bad","base_url":"https://x","operations":[{"resource":"","operation":"o","title":"O","method":"GET","path":"/{{ p.x }}"}]}`), 0o644)
	out, err = runIntegration(t, true, "validate", bad)
	var ce *cliError
	if !errors.As(err, &ce) || ce.code != 3 || !strings.Contains(out, `\"x\" is not one of the op's fields`) {
		t.Errorf("invalid file: %v %s", err, out)
	}
}

func TestIntegrationImportOpenAPI(t *testing.T) {
	home := integrationHome(t)
	spec := "../../internal/apipkg/testdata/openapi/petstore.yaml"
	outDir := t.TempDir()
	out, err := runIntegration(t, false, "import-openapi", spec, "--id", "petstore", "--ops", "store", "--out", outDir)
	if err != nil || !strings.Contains(out, "2 operations") || !strings.Contains(out, "warning:") {
		t.Fatalf("--out: %v %s", err, out)
	}
	if _, err := runIntegration(t, false, "validate", filepath.Join(outDir, "petstore.json")); err != nil {
		t.Errorf("the written package must validate: %v", err)
	}
	// Without --out it is installed and becomes a node type at once.
	if _, err := runIntegration(t, false, "import-openapi", spec, "--id", "petstore"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "apipkgs", "petstore", "package.json")); err != nil {
		t.Fatal(err)
	}
	if p, ok := apipkg.ForNodeType("app.petstore"); !ok || len(p.Operations) != 9 {
		t.Errorf("installed package not loaded")
	}
	if _, err := runIntegration(t, false, "import-openapi", "/nope.yaml"); exitCodeFor(err) != 2 {
		t.Errorf("missing file: %v", err)
	}
}

func TestAppNodesInSchemaListAndPalette(t *testing.T) {
	integrationHome(t)
	var buf strings.Builder
	cmd := newNodeCmd(&globalConfig{JSONOutput: true})
	cmd.SetArgs([]string{"schema", "app.todoist"})
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil || !strings.Contains(buf.String(), `"credential_platform": "todoist"`) {
		t.Fatalf("node schema app.todoist: %v\n%.300s", err, buf.String())
	}
	if nodeTypeCategory("app.todoist") != "app" || nodeCategory("app.todoist") != "app" {
		t.Error("node list category")
	}
	pal := nodePalette(nil, false)
	var found *paletteNode
	for i, n := range pal["app"] {
		if n.Type == "app.todoist" {
			found = &pal["app"][i]
		}
	}
	if found == nil || found.Label != "Todoist" || found.Description == "" || found.Schema == nil || len(found.Schema.Fields) < 10 {
		t.Errorf("palette app.todoist: %+v", found)
	}
}

func TestRefNodeDocumentsAppNodes(t *testing.T) {
	integrationHome(t)
	d := findNodeDoc("app.todoist")
	if d == nil || !strings.Contains(d.Description, "resource=task operation=create") || !strings.Contains(d.Description, "content*") ||
		!strings.Contains(d.Config, `"credential_id": "todoist"`) || !strings.Contains(d.Notes, "connect todoist") {
		t.Fatalf("ref node app.todoist: %+v", d)
	}
	if findNodeDoc("app.nope") != nil {
		t.Error("unknown app type")
	}
}
