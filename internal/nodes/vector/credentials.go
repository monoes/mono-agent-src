package vector

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/vault"
)

// resolveNestedCredential merges a saved connection into cfg when cfg holds
// a credential_id. The engine does this for a node's top-level config only;
// vector.store's nested "embedding" block needs the same treatment for the
// embedding provider's key. Values already set in cfg win over the
// connection's, mirroring how an explicit field overrides a credential.
//
// Without a database in ctx (unit tests, ad-hoc runs) the block is used as
// given.
func resolveNestedCredential(ctx context.Context, cfg map[string]interface{}) error {
	credID := str(cfg, "credential_id")
	if credID == "" {
		return nil
	}
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil
	}
	conn, err := connections.NewStore(db).GetOrResolve(ctx, credID, vault.ProfileIDFromContext(ctx))
	if err != nil {
		return fmt.Errorf("embedding.credential_id %q: %w", credID, err)
	}
	if conn == nil {
		return fmt.Errorf("embedding.credential_id %q: no such connection in this profile", credID)
	}
	for k, v := range conn.Data {
		if _, set := cfg[k]; !set || cfg[k] == "" {
			cfg[k] = v
		}
	}
	return nil
}
