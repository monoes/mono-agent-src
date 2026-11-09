package nodes

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/browser"
)

// A boot that finds its registry already booted puts the action loader back
// (something may have set another since), and a boot of the default home
// boots the one $HOME points at now, not the one it pointed at before.
func TestBootAutomations_ACacheHitIsTheRegistryOfTheHomeItNames(t *testing.T) {
	prevSeed := automation.TestSeed
	automation.TestSeed = nil // the official packages are beside the point, and slow to seed
	bootMu.Lock()
	bootedReg, bootedHome = nil, ""
	bootMu.Unlock()
	t.Cleanup(func() {
		bootMu.Lock()
		bootedReg, bootedHome = nil, ""
		bootMu.Unlock()
		action.SetDefSource(nil)
		browser.SetStartURLResolver(nil)
		automation.TestSeed = prevSeed
	})

	first := t.TempDir()
	installPackage(t, filepath.Join(first, ".monoagent"), "acme-one", "list_deals")
	testhome.SetHome(t, first)
	if _, err := BootAutomations(""); err != nil {
		t.Fatal(err)
	}
	action.SetDefSource(nil) // as a test, or anything else, may
	browser.SetStartURLResolver(nil)
	if _, err := BootAutomations(""); err != nil {
		t.Fatal(err)
	}
	if got := browser.StartURL("acme-one"); got != "https://app.example.test/" {
		t.Errorf("a boot that found its registry booted left the start-URL resolver unset: StartURL = %q", got)
	}
	if src := action.CurrentDefSource(); src == nil {
		t.Fatal("a boot that found its registry booted left the loader unset")
	} else if _, err := src.Load("acme-one", "list_deals"); err != nil {
		t.Fatalf("load through the booted registry: %v", err)
	}

	second := t.TempDir()
	installPackage(t, filepath.Join(second, ".monoagent"), "acme-two", "list_deals")
	testhome.SetHome(t, second)
	reg, err := BootAutomations("")
	if err != nil {
		t.Fatal(err)
	}
	if reg.Home() != filepath.Join(second, ".monoagent") {
		t.Errorf("the default home booted %s, want the one $HOME points at now", reg.Home())
	}
	if _, err := action.CurrentDefSource().Load("acme-two", "list_deals"); err != nil {
		t.Errorf("load a package of the new home: %v", err)
	}
	if _, err := action.CurrentDefSource().Load("acme-one", "list_deals"); err == nil {
		t.Error("the old home's package still loads")
	}
}

// installPackage installs a one-action local package into the registry at home.
func installPackage(t *testing.T, home, id, actionType string) {
	t.Helper()
	reg, err := automation.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), id)
	for name, body := range map[string]string{
		"automation.json": `{"schema": "monoagent.automation/v1", "id": "` + id + `", "name": "` + id + `", "version": "1.0.0",
		  "site": {"startUrl": "https://app.example.test/", "domains": ["app.example.test"]},
		  "permissions": {"steps": ["navigate"], "scripts": [], "downloads": false},
		  "actions": ["` + actionType + `"], "policy": {"tier": "standard"}}`,
		filepath.Join("actions", actionType+".json"): `{"actionType": "` + actionType + `", "automation": "` + id + `", "sideEffects": "read",
		  "steps": [{"id": "open", "type": "navigate", "url": "https://app.example.test/"}]}`,
	} {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := reg.Install(src, automation.InstallOptions{Source: automation.SourceLocal}); err != nil || !res.Installed {
		t.Fatalf("install %s: %v %+v", id, err, res)
	}
}
