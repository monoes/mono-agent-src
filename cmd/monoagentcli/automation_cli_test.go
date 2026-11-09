package main

import (
	"bytes"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	automationcli "github.com/monoes/mono-agent/internal/cli/automation"
	"github.com/monoes/mono-agent/internal/operator"
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
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
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

// stubPickerBridge is a bridge that never connects: a rerecord that reaches
// it fails there.
type stubPickerBridge struct{ browserpkg.ExtensionBridge }

func (stubPickerBridge) IsConnected() bool { return false }

// `automation rerecord` opens a package's site, or any --url, in the user's
// browser and rewrites the selector the click picks, a built-in package's
// too; `automation doctor accept-repair` rewrites one without a click, and
// `automation trust --scripts|--live` widens what a package may do there.
// Through the CLI's own wiring (automationDeps, whose Refusal is
// operator.Refusal) and a real marker, an agent's are refused before the
// registry or the bridge (exit 3, operator_only), and its `automation trust`
// with no flag still shows the flags; the user's rerecord reaches the bridge,
// the user's accept-repair the missing proposal, and the user's trust change
// is stored.
func TestAutomationRerecordRefusesAnAgentThroughTheCLI(t *testing.T) {
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	installTestAutomation(t)
	cfg := &globalConfig{DBPath: filepath.Join(home, ".monoagent", "monoagent.db"), ProfileID: "default", JSONOutput: true}
	run := func(args ...string) (out string, err error, bridge bool) {
		t.Helper()
		d := automationDeps(cfg)
		d.SetupBridge = func(string, zerolog.Logger, time.Duration) browserpkg.ExtensionBridge {
			bridge = true
			return stubPickerBridge{}
		}
		d.EnsureConnected = func(browserpkg.ExtensionBridge, time.Duration) error { return errors.New("bridge stub") }
		cmd := &cobra.Command{Use: "automation"}
		cmd.AddCommand(automationcli.Commands(d)...)
		for _, sub := range cmd.Commands() {
			d.WithJSONErrors(sub)
		}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs(args)
		err = cmd.Execute()
		return buf.String(), err, bridge
	}

	for _, marker := range []string{"CLAUDECODE", "AI_AGENT"} {
		if !slices.Contains(operator.MarkerEnv(), marker) {
			t.Fatalf("%s is not an operator marker", marker)
		}
		t.Setenv(marker, "1")
		for _, args := range [][]string{
			{"rerecord", "acme-test", "page.title"},
			{"rerecord", "acme-test", "page.title", "--url", "https://example.test/account"},
			{"doctor", "accept-repair", "acme-test", "page.title"},
			{"trust", "acme-test", "--scripts"}, {"trust", "acme-test", "--live"}, {"trust", "acme-test", "--no-live"},
		} {
			out, err, bridge := run(args...)
			if exitCodeFor(err) != 3 || !strings.Contains(out, `"operator_only"`) || bridge {
				t.Errorf("%s: %v: exit %d, bridge %v, out %s; want exit 3 operator_only before the bridge", marker, args, exitCodeFor(err), bridge, out)
			}
		}
		if out, err, _ := run("trust", "acme-test"); err != nil || !strings.Contains(out, `"scriptsAllowed": false`) {
			t.Errorf("%s: trust with no flag: %v, out %s; want the flags shown", marker, err, out)
		}
		t.Setenv(marker, "")
	}
	if out, err, bridge := run("rerecord", "acme-test", "page.title"); !bridge || err == nil || !strings.Contains(err.Error(), "bridge stub") {
		t.Errorf("the user's rerecord: %v, bridge %v, out %s; want it to reach the bridge", err, bridge, out)
	}
	if out, err, _ := run("doctor", "accept-repair", "acme-test", "page.title"); err == nil || strings.Contains(out, "operator_only") {
		t.Errorf("the user's accept-repair: %v, out %s; want it past the check, to the missing proposal", err, out)
	}
	if out, err, _ := run("trust", "acme-test", "--scripts"); err != nil || !strings.Contains(out, `"scriptsAllowed": true`) {
		t.Errorf("the user's trust --scripts: %v, out %s; want it stored", err, out)
	}
}
