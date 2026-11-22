package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

func providerDB(t *testing.T) *sql.DB { return newDB(t) }

func TestProviderSelection(t *testing.T) {
	db := providerDB(t)
	if got := SelectedProvider(db).ID; got != jev.ProviderTypeSafe {
		t.Fatalf("default = %q", got)
	}
	if err := UseProvider(db, "openai"); err == nil {
		t.Fatal("a provider without an adapter must be refused")
	}
	if err := UseProvider(db, "nope"); err == nil {
		t.Fatal("unknown provider must be refused")
	}
	if err := UseProvider(db, "custom"); err != nil {
		t.Fatal(err)
	}
	if got := SelectedProvider(db).ID; got != "custom" {
		t.Fatalf("selected = %q", got)
	}
}

func TestProviderEndpointAndClient(t *testing.T) {
	db := providerDB(t)
	if err := SetProviderEndpoint(db, "custom", "http://example.com", ""); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
	if err := SetProviderEndpoint(db, "custom", "http://127.0.0.1:9000/", "m1"); err != nil {
		t.Fatal(err)
	}
	_ = UseProvider(db, "custom")
	c, err := BuildClient(db, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://127.0.0.1:9000" || c.Model != "m1" {
		t.Fatalf("client = %s %s", c.BaseURL, c.Model)
	}
	// A provider that needs a base URL and has none is not configured.
	_ = UseProvider(db, "liquid")
	if _, err := BuildClient(db, "k", ""); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestProviderKeyNeverFallsBackToTypeSafe(t *testing.T) {
	db := providerDB(t)
	t.Setenv("TYPESAFE_API_KEY", "typesafe-secret")
	_ = UseProvider(db, "liquid")
	if _, _, err := ResolveKey(context.Background(), db, "p", ""); !errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("err = %v (the TypeSafe env key must not reach another provider)", err)
	}
	_ = UseProvider(db, "custom")
	key, src, err := ResolveKey(context.Background(), db, "p", "")
	if err != nil || key != "" || src != "none" {
		t.Fatalf("custom without key = %q %q %v", key, src, err)
	}
}
