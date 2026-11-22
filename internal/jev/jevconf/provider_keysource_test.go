package jevconf

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// ProviderKeySource follows the surface's provider, as NewClient does: a
// keyless provider needs no key, a keyed one its machine-vault entry (listed,
// never read), TypeSafe is KeySource.
func TestProviderKeySourceFollowsTheSurfacesProvider(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	ctx := context.Background()
	kind := fakeKind(t, jevtest.NewFakeBackend(nil), nil)
	saveLocal(t, db, "keyed", kind, "openai")
	saveLocal(t, db, "keyless", kind, "")
	t.Setenv("TYPESAFE_API_KEY", "")

	if p, _, err := ProviderKeySource(ctx, db, "default", APIAuto); p != jev.ProviderTypeSafe || !errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("typesafe, no key: %q %v", p, err)
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	if p, src, err := ProviderKeySource(ctx, db, "default", APIAuto); p != jev.ProviderTypeSafe || src != SourceEnv || err != nil {
		t.Fatalf("typesafe from env: %q %q %v", p, src, err)
	}
	if err := SetSurfaceProvider(ctx, db, "default", APIAuto, "keyless"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	if p, src, err := ProviderKeySource(ctx, db, "default", APIAuto); p != "keyless" || src != SourceKeyless || err != nil {
		t.Fatalf("keyless: %q %q %v", p, src, err)
	}
	// A TypeSafe key, or a profile's entry of the same name, does not make a keyed provider available.
	if err := SetSurfaceProvider(ctx, db, "default", APIAuto, "keyed"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	seedKey(t, db, "default", "openai", "profile-openai", "2026-01-01T00:00:00Z")
	if p, _, err := ProviderKeySource(ctx, db, "default", APIAuto); p != "keyed" || !errors.Is(err, jev.ErrNoAPIKey) || !strings.Contains(err.Error(), `"openai"`) {
		t.Fatalf("keyed, no machine entry: %q %v", p, err)
	}
	seedKey(t, db, VaultScope, "openai", "machine-openai", "2026-01-01T00:00:00Z")
	if p, src, err := ProviderKeySource(ctx, db, "default", APIAuto); p != "keyed" || src != SourceVault || err != nil {
		t.Fatalf("keyed: %q %q %v", p, src, err)
	}
	if p, src, err := ProviderKeySource(ctx, db, "default", HIL); p != jev.ProviderTypeSafe || src != SourceEnv || err != nil {
		t.Fatalf("another surface follows the machine default: %q %q %v", p, src, err)
	}
	// By name: "" is the machine default; an unknown provider fails as building its client does.
	if err := SetDefaultProvider(ctx, db, "keyless"); err != nil {
		t.Fatal(err)
	}
	if src, err := ProviderKeySourceOf(ctx, db, "default", ""); src != SourceKeyless || err != nil {
		t.Fatalf("default by name: %q %v", src, err)
	}
	if _, err := ProviderKeySourceOf(ctx, db, "default", "gone"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown: %v", err)
	}
}
