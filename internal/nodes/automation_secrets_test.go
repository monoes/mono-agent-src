package nodes

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
)

// fakeVault swaps the vault for a map keyed by "<profile>|<name>".
func fakeVault(t *testing.T, entries map[string]string) *[]string {
	t.Helper()
	var asked []string
	prev := resolveSecret
	resolveSecret = func(_ context.Context, _ *sql.DB, profileID, name string) (string, error) {
		asked = append(asked, name)
		if v, ok := entries[profileID+"|"+name]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { resolveSecret = prev })
	return &asked
}

// Without a registry that vouches for the automation, only its own
// namespaced secrets resolve (fail closed).
func TestSecretLookup_UnknownAutomationFailsClosed(t *testing.T) {
	action.SetDefSource(nil)
	asked := fakeVault(t, map[string]string{
		"p1|automation:acme-crm/api_key": "namespaced",
		"p1|shared_token":                "bare",
		"p2|automation:acme-crm/api_key": "other-profile",
	})
	look := secretLookup(context.Background(), nil, "p1", "acme-crm")

	if v, ok := look("api_key"); !ok || v != "namespaced" {
		t.Errorf("api_key = %q %v, want the namespaced value", v, ok)
	}
	if _, ok := look("shared_token"); ok {
		t.Error("unknown automation read a bare vault secret")
	}
	if _, ok := look("missing"); ok {
		t.Error("missing secret resolved")
	}
	if _, ok := look("  "); ok {
		t.Error("empty name resolved")
	}
	for _, n := range *asked {
		if n == "shared_token" {
			t.Error("bare name was looked up for an unknown automation")
		}
	}
}

// An imported package reads only secrets filed under its own id.
func TestSecretLookup_ImportedPackageNoBareFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	reg, err := BootAutomations(filepath.Join(home, ".monoagent"))
	t.Cleanup(func() { action.SetDefSource(nil) })
	if err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	src := filepath.Join(t.TempDir(), "acme-crm")
	writeFile(t, filepath.Join(src, "automation.json"), `{
	  "schema": "monoagent.automation/v1",
	  "id": "acme-crm", "name": "Acme CRM", "version": "1.0.0",
	  "site": {"startUrl": "https://app.acme-crm.com/", "domains": ["app.acme-crm.com"]},
	  "permissions": {"steps": ["navigate"], "scripts": [], "downloads": false},
	  "actions": ["open"],
	  "policy": {"tier": "standard"}
	}`)
	writeFile(t, filepath.Join(src, "actions", "open.json"), `{
	  "actionType": "open", "automation": "acme-crm", "sideEffects": "none",
	  "steps": [{"id": "open", "type": "navigate", "url": "https://app.acme-crm.com/"}]
	}`)
	if res, err := reg.Install(src, automation.InstallOptions{}); err != nil || !res.Installed {
		t.Fatalf("Install: %v %+v", err, res)
	}

	asked := fakeVault(t, map[string]string{
		"p1|automation:acme-crm/api_key": "namespaced",
		"p1|github_token":                "must-not-leak",
	})
	look := SecretLookup(context.Background(), nil, "p1", " Acme-CRM ")
	if v, ok := look("api_key"); !ok || v != "namespaced" {
		t.Errorf("api_key = %q %v", v, ok)
	}
	if v, ok := look("github_token"); ok {
		t.Errorf("imported package read a bare vault secret: %q", v)
	}
	for _, n := range *asked {
		if n == "github_token" {
			t.Error("bare name was even looked up for an imported package")
		}
	}

	// A built-in keeps the bare fallback, after its namespaced name.
	builtin := secretLookup(context.Background(), nil, "p1", "gemini")
	if _, ok := builtin("github_token"); !ok {
		t.Error("built-in automation lost the bare-name fallback")
	}
}

// trustPkg is a package context that reports a trust tier.
type trustPkg struct {
	action.PackageContext
	trust string
}

func (p trustPkg) Trust() string { return p.trust }

type trustSource struct {
	fakeSource
	trust map[string]string
}

func (s trustSource) Package(id string) action.PackageContext {
	if t, ok := s.trust[id]; ok {
		return trustPkg{trust: t}
	}
	return nil
}

func TestSecretLookup_BareOnlyForBuiltinAndLocal(t *testing.T) {
	action.SetDefSource(trustSource{trust: map[string]string{
		"b": "builtin", "l": "local", "r": "recorded", "i": "imported", "x": "",
	}})
	t.Cleanup(func() { action.SetDefSource(nil) })
	fakeVault(t, map[string]string{"p1|token": "bare"})

	for id, want := range map[string]bool{"b": true, "l": true, "r": false, "i": false, "x": false, "missing": false} {
		_, got := secretLookup(context.Background(), nil, "p1", id)("token")
		if got != want {
			t.Errorf("trust of %q: bare fallback = %v, want %v", id, got, want)
		}
	}
}

// The executor passes the running step's automation; each gets its own
// namespace and trust, and an empty scope (legacy action) uses the node's.
func TestScopedSecretLookup(t *testing.T) {
	action.SetDefSource(trustSource{trust: map[string]string{"b": "builtin", "i": "imported"}})
	t.Cleanup(func() { action.SetDefSource(nil) })
	fakeVault(t, map[string]string{
		"p1|automation:b/key": "b-key", "p1|automation:i/key": "i-key", "p1|shared": "bare",
	})
	look := ScopedSecretLookup(context.Background(), nil, "p1", "b", nil)
	for _, c := range []struct {
		scope, name, want string
		ok                bool
	}{
		{"b", "key", "b-key", true},
		{"i", "key", "i-key", true},
		{"b", "shared", "bare", true},
		{"i", "shared", "", false},
		{"", "key", "b-key", true}, // falls back to the node's automation
	} {
		v, ok := look(c.scope, c.name)
		if v != c.want || ok != c.ok {
			t.Errorf("look(%q,%q) = %q %v, want %q %v", c.scope, c.name, v, ok, c.want, c.ok)
		}
	}
}
