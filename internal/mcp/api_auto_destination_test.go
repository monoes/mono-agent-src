package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// S6: the refusals of api_auto_set name where the data goes: TypeSafe, as
// before decision providers, or the api_auto surface's own provider and its
// host, so an acknowledgement is given against the real destination.
func TestAPIAutoSetNamesTheSurfacesProviderAndHost(t *testing.T) {
	for _, exposure := range []bool{true, false} {
		f := newConfigFixture(t, configSetup{allowExposure: exposure})
		_, err := f.call("api_auto_set", map[string]any{"enabled": true})
		if err == nil || !strings.Contains(err.Error(), "sends data to TypeSafe (Jev) for every request") {
			t.Fatalf("exposure %v, nothing configured: %v", exposure, err)
		}
	}

	fb := jevtest.NewFakeBackend(nil)
	t.Cleanup(jevconf.RegisterKind("mcp-fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	for _, exposure := range []bool{true, false} {
		f := newConfigFixture(t, configSetup{allowExposure: exposure})
		ctx := context.Background()
		if err := jevconf.SaveProvider(ctx, f.Side.DB, jevconf.Provider{Name: "remote", Kind: "mcp-fake",
			BaseURL: "https://decide.example.com/v1", SecretName: "decision-remote"}); err != nil {
			t.Fatal(err)
		}
		if err := jevconf.SetSurfaceProvider(ctx, f.Side.DB, "default", jevconf.APIAuto, "remote"); err != nil {
			t.Fatal(err)
		}
		_, err := f.call("api_auto_set", map[string]any{"enabled": true})
		if err == nil || !strings.Contains(err.Error(), `the Jev decision provider "remote" at decide.example.com`) || strings.Contains(err.Error(), "TypeSafe") {
			t.Fatalf("exposure %v, provider remote: %v", exposure, err)
		}
	}
}
