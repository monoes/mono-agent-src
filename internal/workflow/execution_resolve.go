package workflow

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// resolveNodeConfig is where every node config finishes resolving, whichever
// path produced it (the batch, each item of a per-item run, a loop
// iteration, a sub-node, an ai_tool call): the stored-field and connection
// checks, the vault and @secret references, and the egress check on what is
// left (CheckEgress). resolved already has its expressions evaluated; stored
// is the config as the workflow holds it. ctx names the step
// (secrets.WithAccessNode / WithAccessNodeType). A path of the engine that
// resolves @secret references any other way fails
// TestEverySecretResolutionGoesThroughResolveNodeConfig.
func resolveNodeConfig(ctx context.Context, nodeType string, executor NodeExecutor, cred *CredentialUse, stored, resolved map[string]interface{}) error {
	if err := checkStoredFields(executor, stored, resolved); err != nil {
		return err
	}
	if err := checkCredentialUse(ctx, executor, cred, stored, resolved); err != nil {
		return err
	}
	if db := vault.DBFromContext(ctx); db != nil {
		profile, perr := vault.ProfileIDFromContext(ctx)
		if perr != nil {
			return fmt.Errorf("ran without a profile: %w", perr)
		}
		// @img-NNN references to absolute vault file paths.
		if err := vault.ResolveConfig(ctx, db, stored, resolved); err != nil {
			return err
		}
		// @secret:<name> references to decrypted vault values; a run the user
		// hasn't approved resolves none, and the step doesn't run.
		if err := secrets.ResolveConfig(ctx, db, profile, stored, resolved); err != nil {
			return err
		}
	}
	var keys [][]string
	if d, ok := executor.(CredentialDestinations); ok {
		keys = d.CredentialDestinationKeys()
	}
	return CheckEgress(ctx, nodeType, keys, resolved)
}
