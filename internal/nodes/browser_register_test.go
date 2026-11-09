package nodes

import (
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeSource is a DefSource with a fixed action list and no packages.
type fakeSource struct{ list []string }

func (f fakeSource) Load(a, t string) ([]byte, error) {
	return nil, fmt.Errorf("fake: %s/%s", a, t)
}
func (f fakeSource) List() ([]string, error)                         { return f.list, nil }
func (f fakeSource) Package(automation string) action.PackageContext { return nil }

func TestRegisterBrowserNodes_FromDefSource(t *testing.T) {
	action.SetDefSource(fakeSource{list: []string{
		"acme-crm/create_contact",
		"acme-crm/create_contact", // duplicates must not panic
		"local-widgets/scrape",
		"local-acme-crm/create_contact", // "acme-crm.create_contact" is taken
	}})
	t.Cleanup(func() { action.SetDefSource(nil) })

	r := workflow.NewNodeTypeRegistry()
	RegisterBrowserNodes(r)

	for _, nt := range []string{"acme-crm.create_contact", "local-widgets.scrape", "local-acme-crm.create_contact"} {
		if !r.Has(nt) {
			t.Errorf("node type %q not registered", nt)
		}
	}
	// A legacy ~/.monoagent/actions/widgets/scrape.json workflow node keeps
	// resolving after the file was wrapped into local-widgets.
	if !r.Has("widgets.scrape") {
		t.Error("legacy name widgets.scrape does not resolve")
	}
	f, ok := r.Get("widgets.scrape")
	if !ok {
		t.Fatal("widgets.scrape: no factory")
	}
	if got := f().(*BrowserNode).platform; got != "local-widgets" {
		t.Errorf("widgets.scrape runs automation %q, want local-widgets", got)
	}
	// The alias is hidden from the palette.
	for _, nt := range r.Types() {
		if nt == "widgets.scrape" {
			t.Error("alias listed in Types()")
		}
	}
}

// legacyPkg is a local-* package context that knows its original name.
type legacyPkg struct {
	action.PackageContext
	name string
}

func (p legacyPkg) LegacyPlatform() string { return p.name }

type legacySource struct {
	fakeSource
	legacy map[string]string
}

func (s legacySource) Package(id string) action.PackageContext {
	if n, ok := s.legacy[id]; ok {
		return legacyPkg{name: n}
	}
	return nil
}

// An upgraded ~/.monoagent/actions/google_maps/get_place.json becomes
// package local-google-maps; workflows still say google_maps.get_place.
func TestRegisterBrowserNodes_LegacyAliasUsesOriginalName(t *testing.T) {
	action.SetDefSource(legacySource{
		fakeSource: fakeSource{list: []string{"local-google-maps/get_place", "local-widgets/scrape"}},
		legacy:     map[string]string{"local-google-maps": "google_maps"},
	})
	t.Cleanup(func() { action.SetDefSource(nil) })

	r := workflow.NewNodeTypeRegistry()
	RegisterBrowserNodes(r)
	if !r.Has("google_maps.get_place") {
		t.Error("google_maps.get_place (the original name) does not resolve")
	}
	if f, ok := r.Get("google_maps.get_place"); ok {
		if got := f().(*BrowserNode).platform; got != "local-google-maps" {
			t.Errorf("runs automation %q, want local-google-maps", got)
		}
	}
	// No recorded name: the id without "local-", as before.
	if !r.Has("widgets.scrape") {
		t.Error("widgets.scrape does not resolve")
	}
}

// Upgrade path against the real registry: a pre-package
// ~/.monoagent/actions/google_maps/get_place.json is wrapped into a local
// package on boot, and workflows that say google_maps.get_place still find
// (and would run) it.
func TestLegacyUpgrade_GoogleMapsAliasResolves(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	dir := filepath.Join(home, ".monoagent", "actions", "google_maps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "get_place.json"), []byte(`{
	  "actionType": "get_place", "platform": "google_maps",
	  "inputs": {"required": [{"name": "query", "type": "string"}]},
	  "steps": [{"id": "open", "type": "navigate", "url": "https://www.google.com/maps/search/{{query}}"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BootAutomations(filepath.Join(home, ".monoagent")); err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	t.Cleanup(func() { action.SetDefSource(nil) })

	r := workflow.NewNodeTypeRegistry()
	RegisterBrowserNodes(r)
	f, ok := r.Get("google_maps.get_place")
	if !ok {
		t.Fatalf("google_maps.get_place does not resolve; registered: %v", r.Types())
	}
	bn := f().(*BrowserNode)
	if !strings.HasPrefix(bn.platform, "local-google-maps") {
		t.Fatalf("alias runs automation %q, want the wrapped local-google-maps package", bn.platform)
	}
	// The run path finds the definition through the registry.
	def, err := action.GetLoader().Load(bn.platform, bn.actionType)
	if err != nil || def.ActionType != "get_place" {
		t.Fatalf("load %s/%s: %v", bn.platform, bn.actionType, err)
	}
	if r.Has("google-maps.get_place") {
		t.Error("an alias was derived from the sanitised id (google-maps)")
	}
	// Its form offers google_maps' sessions (the legacy login), under
	// both names.
	for _, nt := range []string{"google_maps.get_place", bn.platform + ".get_place"} {
		s, err := workflow.LoadDefaultSchema(nt)
		if err != nil || s.CredentialPlatform == nil || *s.CredentialPlatform != "google_maps" {
			got := "<nil>"
			if s != nil && s.CredentialPlatform != nil {
				got = *s.CredentialPlatform
			}
			t.Errorf("%s: credential_platform %s, want google_maps (%v)", nt, got, err)
		}
	}
}

func TestWebAutomations_ListsEveryAutomationWithActions(t *testing.T) {
	action.SetDefSource(fakeSource{list: []string{
		"acme-crm/create_contact",
		"acme-crm/list_contacts",
		"local-widgets/scrape",
		"broken-entry",
	}})
	t.Cleanup(func() { action.SetDefSource(nil) })

	got := WebAutomations()
	if len(got) != 2 || got["acme-crm"] != "acme-crm" || got["local-widgets"] != "local-widgets" {
		t.Fatalf("WebAutomations() = %v", got)
	}
}
