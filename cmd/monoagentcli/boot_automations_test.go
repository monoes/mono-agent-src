package main

import (
	"bytes"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/nodes"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(withTestProfile(args))
	err := root.Execute()
	return out.String(), err
}

// TestCLI_BootsInstalledAutomations: a user package installed into the
// registry is visible to CLI paths that read actions without building a
// node registry first (node schema) and to workflow validation.
func TestCLI_BootsInstalledAutomations(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	skipAutomationBoot = false
	t.Cleanup(func() { skipAutomationBoot = true; action.SetDefSource(nil) })

	reg, err := automation.Open(filepath.Join(home, ".monoagent"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "acme-crm")
	writeTestFile(t, filepath.Join(src, "automation.json"), `{
	  "schema": "monoagent.automation/v1",
	  "id": "acme-crm", "name": "Acme CRM", "version": "1.0.0",
	  "site": {"startUrl": "https://app.acme-crm.com/", "domains": ["app.acme-crm.com"]},
	  "permissions": {"steps": ["navigate"], "scripts": [], "downloads": false},
	  "actions": ["list_deals"],
	  "policy": {"tier": "standard"}
	}`)
	writeTestFile(t, filepath.Join(src, "actions", "list_deals.json"), `{
	  "actionType": "list_deals", "automation": "acme-crm", "sideEffects": "read",
	  "inputs": {"optional": [{"name": "stage", "type": "string", "enum": ["open", "won"]}]},
	  "steps": [{"id": "open", "type": "navigate", "url": "https://app.acme-crm.com/deals"}]
	}`)
	if res, err := reg.Install(src, automation.InstallOptions{Source: automation.SourceLocal}); err != nil || !res.Installed {
		t.Fatalf("Install: %v %+v", err, res)
	}
	action.SetDefSource(nil) // the CLI must boot it itself

	out, err := runRoot(t, "--db-path", filepath.Join(home, "t.db"), "node", "schema", "acme-crm.list_deals")
	if err != nil {
		t.Fatalf("node schema: %v\n%s", err, out)
	}
	var schema struct {
		Fields []struct{ Key, Type string } `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &schema); err != nil {
		t.Fatalf("node schema output: %v\n%s", err, out)
	}
	var stage bool
	for _, f := range schema.Fields {
		stage = stage || (f.Key == "stage" && f.Type == "select")
	}
	if !stage {
		t.Errorf("generated form lacks the stage select:\n%s", out)
	}
	if action.CurrentDefSource() == nil {
		t.Error("root command did not boot the automation registry")
	}

	wf := filepath.Join(t.TempDir(), "wf.json")
	writeTestFile(t, wf, `{
	  "name": "deals",
	  "nodes": [
	    {"id": "t", "type": "trigger.manual", "name": "Start", "config": {}},
	    {"id": "d", "type": "acme-crm.list_deals", "name": "Deals", "config": {"stage": "open"}}
	  ],
	  "connections": [{"source_node_id": "t", "target_node_id": "d"}]
	}`)
	out, err = runRoot(t, "--db-path", filepath.Join(home, "t.db"), "workflow", "validate", "--file", wf)
	if err != nil || !strings.Contains(out, "valid") {
		t.Fatalf("workflow validate: %v\n%s", err, out)
	}
}

func TestNeedsAutomations(t *testing.T) {
	root := newRootCmd()
	root.InitDefaultCompletionCmd()
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"node", "schema"}, true},
		{[]string{"workflow", "validate"}, true},
		{[]string{"mcp"}, true},
		{[]string{"httpapi"}, true},
		{[]string{"daemon"}, true},
		{[]string{"completion", "bash"}, false},
	} {
		cmd, _, err := root.Find(c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got := needsAutomations(cmd); got != c.want {
			t.Errorf("needsAutomations(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestNeedsBrowserSession: `node run` sets up the browser for every
// automation action, including an installed package that isn't a built-in.
func TestNeedsBrowserSession(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	reg, err := nodes.BootAutomations(filepath.Join(home, ".monoagent"))
	t.Cleanup(func() { action.SetDefSource(nil) })
	if err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	src := filepath.Join(t.TempDir(), "acme-crm")
	writeTestFile(t, filepath.Join(src, "automation.json"), `{
	  "schema": "monoagent.automation/v1",
	  "id": "acme-crm", "name": "Acme CRM", "version": "1.0.0",
	  "site": {"startUrl": "https://app.acme-crm.com/", "domains": ["app.acme-crm.com"]},
	  "permissions": {"steps": ["navigate"], "scripts": [], "downloads": false},
	  "actions": ["list_deals"],
	  "policy": {"tier": "standard"}
	}`)
	writeTestFile(t, filepath.Join(src, "actions", "list_deals.json"), `{
	  "actionType": "list_deals", "automation": "acme-crm", "sideEffects": "read",
	  "steps": [{"id": "open", "type": "navigate", "url": "https://app.acme-crm.com/deals"}]
	}`)
	if res, err := reg.Install(src, automation.InstallOptions{Source: automation.SourceLocal}); err != nil || !res.Installed {
		t.Fatalf("Install: %v %+v", err, res)
	}

	registry := buildNodeRegistry(false, nil)
	for nt, want := range map[string]bool{
		"acme-crm.list_deals":  true,
		"gemini.generate_text": true,
		"core.if":              false,
	} {
		f, ok := registry.Get(nt)
		if !ok {
			t.Fatalf("%s not registered", nt)
		}
		if got := needsBrowserSession(nt, f); got != want {
			t.Errorf("needsBrowserSession(%s) = %v, want %v", nt, got, want)
		}
	}
}

// TestNodePalette_InstalledAutomationGetsItsOwnSection: a web automation
// installed after the fact lands in the palette's web-automation section,
// under its own name, and the built-in nodes stay in the other section.
func TestNodePalette_InstalledAutomationGetsItsOwnSection(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	skipAutomationBoot = false
	t.Cleanup(func() { skipAutomationBoot = true; action.SetDefSource(nil) })

	reg, err := automation.Open(filepath.Join(home, ".monoagent"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "acme-crm")
	writeTestFile(t, filepath.Join(src, "automation.json"), `{
	  "schema": "monoagent.automation/v1",
	  "id": "acme-crm", "name": "Acme CRM", "version": "1.0.0",
	  "site": {"startUrl": "https://app.acme-crm.com/", "domains": ["app.acme-crm.com"]},
	  "permissions": {"steps": ["navigate"], "scripts": [], "downloads": false},
	  "actions": ["list_deals"],
	  "policy": {"tier": "standard"}
	}`)
	writeTestFile(t, filepath.Join(src, "actions", "list_deals.json"), `{
	  "actionType": "list_deals", "automation": "acme-crm", "sideEffects": "read",
	  "steps": [{"id": "open", "type": "navigate", "url": "https://app.acme-crm.com/deals"}]
	}`)
	if res, err := reg.Install(src, automation.InstallOptions{Source: automation.SourceLocal}); err != nil || !res.Installed {
		t.Fatalf("Install: %v %+v", err, res)
	}
	// Boot this home under its own key first: the default-home boot is
	// cached per process, so an earlier test's HOME would otherwise answer.
	if _, err := nodes.BootAutomations(filepath.Join(home, ".monoagent")); err != nil {
		t.Fatal(err)
	}
	action.SetDefSource(nil) // the CLI must boot it itself

	out, err := runRoot(t, "--db-path", filepath.Join(home, "t.db"), "node", "palette")
	if err != nil {
		t.Fatalf("node palette: %v\n%s", err, out)
	}
	var got map[string][]paletteNode
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("palette is not JSON: %v\n%s", err, out)
	}
	acme := got["acme-crm"]
	if len(acme) != 1 {
		t.Fatalf("acme-crm = %+v", acme)
	}
	if n := acme[0]; n.Type != "acme-crm.list_deals" || n.Section != paletteSectionWebAutomation ||
		n.CategoryLabel != "Acme CRM" || n.Label != "List Deals" {
		t.Errorf("acme-crm.list_deals = %+v", n)
	}
	for _, group := range []string{"triggers", "control", "data", "http"} {
		for _, n := range got[group] {
			if n.Section != paletteSectionNodes || n.CategoryLabel != "" {
				t.Errorf("%s (%s) is in section %q (%q), want %q", n.Type, group, n.Section, n.CategoryLabel, paletteSectionNodes)
			}
		}
	}
}
