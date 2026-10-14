package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/automation"
	automationcli "github.com/monoes/mono-agent/internal/cli/automation"
)

// list, show, new, test, doctor, trust and rerecord live in internal/cli/automation, with
// their unit tests. The end-to-end tests here run them through newRootCmd and decode their
// --json with these.
type (
	automationListRow    = automationcli.ListRow
	automationActionJSON = automationcli.ActionJSON
	fixtureResult        = automationcli.FixtureResult
)

var (
	countProblems           = automationcli.CountProblems
	automationTemplatesRoot = automationcli.TemplatesRoot
	automationTemplateNames = automationcli.TemplateNames
)

// The workflow bundle tests share these with the doctor tests in internal/cli/automation.

// writeTestAutomation writes a small valid, non-built-in package directory
// ("acme-test") and returns it.
func writeTestAutomation(t *testing.T) string {
	t.Helper()
	return writeTestAutomationID(t, "acme-test")
}

// writeTestAutomationID writes the same package under another id.
func writeTestAutomationID(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	files := map[string]string{
		"automation.json": `{
  "schema": "monoagent.automation/v1",
  "id": "acme-test",
  "name": "Acme Test",
  "version": "1.0.0",
  "site": {"startUrl": "https://example.com/", "domains": ["example.com"]},
  "permissions": {"steps": ["navigate", "extract_text"], "scripts": [], "downloads": false},
  "actions": ["get_title"],
  "policy": {"tier": "standard"}
}
`,
		"actions/get_title.json": `{"actionType":"get_title","automation":"acme-test","sideEffects":"read","steps":[
  {"id":"open","type":"navigate","url":"https://example.com/"},
  {"id":"title","type":"extract_text","configKey":"page.title","variable_name":"title"}
]}
`,
		"selectors.json": `{
  "page.title": {"candidates": [{"css": "h1"}, {"text": "Example Domain"}]},
  "page.link": {"candidates": [{"css": "a"}]}
}
`,
	}
	for name, body := range files {
		body = strings.ReplaceAll(body, "acme-test", id)
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// installTestAutomation installs acme-test into the registry under $HOME.
func installTestAutomation(t *testing.T) {
	t.Helper()
	reg, err := openAutomationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(writeTestAutomation(t), automation.InstallOptions{}); err != nil {
		t.Fatalf("install acme-test: %v", err)
	}
}

// TestAutomationAdapterWiring pins what the adapter hands internal/cli/automation: --db-path
// (~ expanded, never created by doctor), the registry under $HOME, --json errors, and
// `automation rerecord` reaching the registry before any browser.
func TestAutomationAdapterWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installTestAutomation(t)

	out, errOut, err := runAutomationCLI(t, home, "automation", "doctor", "acme-test", "--json")
	if err != nil || !strings.Contains(out, `"id": "acme-test"`) || !strings.Contains(out, `"key": "page.title"`) {
		t.Fatalf("doctor = %q / %q, %v", out, errOut, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "monoagent.db")); err == nil {
		t.Fatal("doctor created the database")
	}
	out, _, err = runAutomationCLI(t, home, "automation", "doctor", "nope", "--json")
	if err == nil || out != "{\"error\":\"automation: not installed: nope\"}\n" {
		t.Fatalf("doctor nope = %q, %v; want the JSON error document", out, err)
	}
	out, _, err = runAutomationCLI(t, home, "automation", "rerecord", "acme-test", "no.such.key", "--json")
	if err == nil || !strings.Contains(out, `automation acme-test has no selector \"no.such.key\"`) {
		t.Fatalf("rerecord unknown key = %q, %v", out, err)
	}
}
