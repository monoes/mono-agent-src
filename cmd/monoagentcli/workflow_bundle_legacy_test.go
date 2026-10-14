package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/rs/zerolog"
)

// D5a: a workflow whose nodes use a legacy action (~/.monoagent/actions/<p>,
// wrapped into a generated local-* package that keeps <p> as its alias) must
// bundle that package under canonical node types, and on a fresh machine the
// imported package must run the node.

// legacyPage is a minimal page with a set of present CSS selectors.
type legacyPage struct {
	browser.PageInterface
	present map[string]bool
	hits    int // lookups that found an element
}

type legacyElement struct{ browser.ElementHandle }

func (p *legacyPage) Element(sel string, _ time.Duration) (browser.ElementHandle, error) {
	if p.present[sel] {
		p.hits++
		return legacyElement{}, nil
	}
	return nil, errors.New("element not found: " + sel)
}
func (p *legacyPage) ElementX(xp string, t time.Duration) (browser.ElementHandle, error) {
	return p.Element(xp, t)
}
func (p *legacyPage) Race(sels []string, _ time.Duration) (int, browser.ElementHandle, error) {
	for i, s := range sels {
		if p.present[s] {
			p.hits++
			return i, legacyElement{}, nil
		}
	}
	return -1, nil, errors.New("none of the selectors matched")
}
func (p *legacyPage) GetURL() (string, error)                     { return "https://example.com/", nil }
func (p *legacyPage) Navigate(string) error                       { return nil }
func (p *legacyPage) Timeout(time.Duration) browser.PageInterface { return p }
func (p *legacyPage) WaitLoad() error                             { return nil }

// writeLegacyHeading writes ~/.monoagent/actions/<platform>/get_heading.json.
func writeLegacyHeading(t *testing.T, home, platform string) {
	t.Helper()
	dir := filepath.Join(home, ".monoagent", "actions", platform)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"actionType":"get_heading","platform":"` + platform + `","steps":[
  {"id":"open","type":"navigate","url":"https://example.com/"},
  {"id":"heading","type":"find_element","selector":"h1","timeout":0.2}
]}`
	if err := os.WriteFile(filepath.Join(dir, "get_heading.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runNodeType loads "<prefix>.<action>" through the registry's DefSource —
// the path a workflow node takes — and runs it on a fake page.
func runNodeType(t *testing.T, prefix, actionName string) {
	t.Helper()
	reg, err := openAutomationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	src := reg.DefSource()
	raw, err := src.Load(prefix, actionName)
	if err != nil {
		t.Fatalf("node %s.%s does not resolve: %v", prefix, actionName, err)
	}
	var def action.ActionDef
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	page := &legacyPage{present: map[string]bool{"h1": true}}
	ae := action.NewActionExecutor(context.Background(), page, nil, nil, nil, nil, zerolog.Nop())
	if pc := src.Package(prefix); pc != nil {
		ae.SetPackage(pc)
	}
	res, err := ae.ExecuteDef(&action.StorageAction{ID: "d5a", Type: actionName, TargetPlatform: prefix}, &def)
	if err != nil {
		t.Fatalf("node %s.%s failed: %v", prefix, actionName, err)
	}
	if res == nil || len(res.FailedItems) != 0 || page.hits == 0 {
		t.Fatalf("node %s.%s did not run its step: result %+v, element hits %d", prefix, actionName, res, page.hits)
	}
}

func TestWorkflowBundleLegacyAlias(t *testing.T) {
	for _, platform := range []string{"examplesite", "example_site"} {
		t.Run(platform, func(t *testing.T) {
			home1 := t.TempDir()
			t.Setenv("HOME", home1)
			writeLegacyHeading(t, home1, platform)
			reg, err := openAutomationRegistry() // seeding wraps the legacy dir
			if err != nil {
				t.Fatal(err)
			}
			pkgID := packageResolver(reg)(platform, "get_heading")
			if pkgID == "" {
				t.Fatalf("prefix %q resolves to no package", platform)
			}
			if _, err := reg.Info(pkgID); err != nil {
				t.Fatalf("resolved %q to %q, which is not installed: %v", platform, pkgID, err)
			}

			cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "src.db"), JSONOutput: true, ProfileID: "default"}
			wf := `{"name":"legacy-wf","nodes":[
  {"id":"t","type":"trigger.manual","name":"Trigger","position":{"x":0,"y":0},"config":{}},
  {"id":"h","type":"` + platform + `.get_heading","name":"Heading","position":{"x":200,"y":0},"config":{}}
],"connections":[{"id":"t-h","source":"t","target":"h"}]}`
			src := filepath.Join(t.TempDir(), "wf.json")
			if err := os.WriteFile(src, []byte(wf), 0o644); err != nil {
				t.Fatal(err)
			}
			var imported struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "import", "--file", src)), &imported); err != nil {
				t.Fatal(err)
			}
			// Default: a legacy package runs unrestricted and has no domains,
			// so it is left out, with a hint listing the suggested domains.
			_, bare := exportBundle(t, cfg, imported.ID)
			if u, ok := bare.Unbundled[pkgID]; !ok || len(bare.Automations) != 0 ||
				!strings.Contains(u.Hint, "example.com") || !strings.Contains(u.Hint, "--use-suggested-domains") {
				t.Fatalf("default export: bundled %v, unbundled %+v", keysOf(bare.Automations), bare.Unbundled)
			}

			bundled := filepath.Join(t.TempDir(), "bundled.json")
			runWorkflowSubcmd(t, cfg, "export", imported.ID, "--bundle-automations", "--use-suggested-domains", "-o", bundled)
			raw, err := os.ReadFile(bundled)
			if err != nil {
				t.Fatal(err)
			}
			var doc workflowBundleFile
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if _, ok := doc.Automations[pkgID]; !ok || len(doc.Automations) != 1 {
				t.Fatalf("bundle holds %v, want exactly %s", keysOf(doc.Automations), pkgID)
			}
			// The bundled workflow names the package it ships, not the local alias.
			if doc.Nodes[1].Type != pkgID+".get_heading" || doc.Nodes[0].Type != "trigger.manual" {
				t.Fatalf("bundled node types: %q, %q", doc.Nodes[0].Type, doc.Nodes[1].Type)
			}
			// On the exporting machine the alias itself still runs.
			runNodeType(t, platform, "get_heading")

			// A fresh machine without the legacy directory.
			t.Setenv("HOME", t.TempDir())
			cfg2 := &globalConfig{DBPath: filepath.Join(t.TempDir(), "dst.db"), JSONOutput: true, ProfileID: "default"}
			var res struct {
				Automations []bundleImportItem `json:"automations"`
			}
			if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg2, "import", "--file", bundled, "--yes")), &res); err != nil {
				t.Fatal(err)
			}
			if len(res.Automations) != 1 || res.Automations[0].ID != pkgID || res.Automations[0].Status != "installed" {
				t.Fatalf("import report: %+v", res.Automations)
			}
			// The imported workflow's node runs from the imported package.
			runNodeType(t, pkgID, "get_heading")
		})
	}
}

// A disabled legacy package is hidden from the DefSource; the resolver still
// finds it through its recorded platform, so the bundle is not silently short.
func TestPackageResolverDisabledLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeLegacyHeading(t, home, "example_site")
	reg, err := openAutomationRegistry()
	if err != nil {
		t.Fatal(err)
	}
	id := packageResolver(reg)("example_site", "get_heading")
	if id == "" {
		t.Fatal("enabled legacy package not resolved")
	}
	if err := reg.SetEnabled(id, false); err != nil {
		t.Fatal(err)
	}
	if got := packageResolver(reg)("example_site", "get_heading"); got != id {
		t.Fatalf("disabled legacy package resolved to %q, want %q", got, id)
	}
	if got := packageResolver(reg)("core", "set"); got != "" {
		t.Fatalf("core resolved to %q", got)
	}
}
