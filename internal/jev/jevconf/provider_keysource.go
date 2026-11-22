package jevconf

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/secrets"
)

// ProviderKeySource is KeySource for the provider surface s uses for
// profileID when the call names none (ActiveProvider's rule), so a check
// agrees with the client NewClient(…, s) builds. See ProviderKeySourceOf.
func ProviderKeySource(ctx context.Context, db *sql.DB, profileID string, s Surface) (provider, source string, err error) {
	provider, err = activeProvider(db, profileID, s)
	if err != nil {
		return provider, "", err
	}
	source, err = ProviderKeySourceOf(ctx, db, profileID, provider)
	return provider, source, err
}

// ProviderKeySourceOf reports where provider's key would come from ("" = the
// machine default), without decrypting anything: TypeSafe is KeySource;
// another provider is SourceKeyless when it needs no key, else SourceVault
// when its machine-vault entry exists (its name is listed, never read). A
// missing key wraps jev.ErrNoAPIKey; an unknown provider, a refused record
// and a kind not in this build fail as ResolveProviderClient does.
func ProviderKeySourceOf(ctx context.Context, db *sql.DB, profileID, provider string) (string, error) {
	if provider == "" {
		var err error
		if provider, err = defaultProvider(db); err != nil {
			return "", err
		}
	}
	if provider == jev.ProviderTypeSafe {
		return KeySource(ctx, db, profileID)
	}
	p, ok, err := FindProvider(db, provider)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w %q (`jev provider list` shows the configured ones)", ErrUnknownProvider, provider)
	}
	if err := checkProvider(p); err != nil {
		return "", err
	}
	if _, ok := builderFor(p.Kind); !ok {
		return "", fmt.Errorf("jev: provider %q is of kind %q, which this build does not include", p.Name, p.Kind)
	}
	if p.SecretName == "" {
		return SourceKeyless, nil
	}
	if db != nil {
		if entries, err := secrets.List(ctx, db, VaultScope); err == nil {
			for _, e := range entries {
				if e.Name == p.SecretName && e.FieldCount > 0 {
					return SourceVault, nil
				}
			}
		}
	}
	return "", noProviderKey{p.Name, p.SecretName}
}
