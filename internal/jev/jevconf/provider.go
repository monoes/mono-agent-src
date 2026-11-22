package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
)

// ErrProviderNotConfigured is returned when the selected provider has no base URL yet.
var ErrProviderNotConfigured = errors.New("jev: the selected decision provider has no base URL; set one with `jev provider set`")

const providerSetting = "jev.provider"

func providerField(id, name string) string { return "jev.provider." + id + "." + name }

// ProviderSecret is the machine-vault entry holding a non-TypeSafe provider's key.
func ProviderSecret(id string) string { return "decision-" + id }

// SelectedProvider is the machine-wide decision provider (TypeSafe until changed).
func SelectedProvider(db *sql.DB) jev.Provider {
	if id, ok := getSetting(db, providerSetting); ok {
		if p, ok := jev.ProviderByID(id); ok && p.Supported() {
			return p
		}
	}
	p, _ := jev.ProviderByID(jev.ProviderTypeSafe)
	return p
}

// UseProvider selects id; a provider without an adapter is refused.
func UseProvider(db *sql.DB, id string) error {
	p, ok := jev.ProviderByID(id)
	if !ok {
		return fmt.Errorf("jev: unknown decision provider %q", id)
	}
	if !p.Supported() {
		return fmt.Errorf("jev: %s is listed but has no adapter yet (only /v1/systemone providers work)", p.Name)
	}
	return setSetting(db, providerSetting, p.ID)
}

// ProviderEndpoint is the base URL and model in effect for p: the user's
// setting, else the catalog default.
func ProviderEndpoint(db *sql.DB, p jev.Provider) (baseURL, model string) {
	baseURL, model = p.BaseURL, ""
	if len(p.Models) > 0 {
		model = p.Models[0]
	}
	if v, ok := getSetting(db, providerField(p.ID, "base_url")); ok && strings.TrimSpace(v) != "" {
		baseURL = strings.TrimSpace(v)
	}
	if v, ok := getSetting(db, providerField(p.ID, "model")); ok && strings.TrimSpace(v) != "" {
		model = strings.TrimSpace(v)
	}
	return baseURL, model
}

// SetProviderEndpoint stores p's base URL and model; "" leaves a field as it was.
func SetProviderEndpoint(db *sql.DB, id, baseURL, model string) error {
	p, ok := jev.ProviderByID(id)
	if !ok || !p.Supported() {
		return fmt.Errorf("jev: %q is not a usable decision provider", id)
	}
	if baseURL = strings.TrimSpace(baseURL); baseURL != "" {
		if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://127.0.0.1") && !strings.HasPrefix(baseURL, "http://localhost") {
			return fmt.Errorf("jev: base URL must be https:// (http:// only for localhost): %q", baseURL)
		}
		if err := setSetting(db, providerField(p.ID, "base_url"), baseURL); err != nil {
			return err
		}
	}
	if model = strings.TrimSpace(model); model != "" {
		return setSetting(db, providerField(p.ID, "model"), model)
	}
	return nil
}

// BuildClient makes a client for the selected provider from an already
// resolved key. It is the one place a key meets a host, so a key never goes to
// a provider other than the one it was resolved for.
func BuildClient(db *sql.DB, key, model string) (*jev.Client, error) {
	p := SelectedProvider(db)
	if p.ID == jev.ProviderTypeSafe {
		return jev.NewClient(key, model)
	}
	base, defModel := ProviderEndpoint(db, p)
	if base == "" {
		return nil, ErrProviderNotConfigured
	}
	if key == "" && p.KeyOptional {
		key = "local"
	}
	if model == "" {
		model = defModel
	}
	c, err := jev.NewClient(key, model)
	if err != nil {
		return nil, err
	}
	c.BaseURL = strings.TrimRight(base, "/")
	c.Protocol = p.Protocol
	return c, nil
}

// resolveProviderKey is ResolveKey for a non-TypeSafe provider: its own
// machine-vault entry only, never the TypeSafe key.
func resolveProviderKey(ctx context.Context, db *sql.DB, p jev.Provider, explicit string) (key, source string, err error) {
	if v := strings.TrimSpace(explicit); v != "" && !strings.HasPrefix(v, "@secret:") {
		return v, SourceConfig, nil
	}
	if db != nil {
		v, found, rerr := entryValue(ctx, db, VaultScope, ProviderSecret(p.ID))
		if rerr != nil {
			return "", "", fmt.Errorf("%w: %v", ErrKeyUnreadable, rerr)
		}
		if found && v != "" {
			return v, SourceVault, nil
		}
	}
	if p.KeyOptional {
		return "", "none", nil
	}
	return "", "", jev.ErrNoAPIKey
}
