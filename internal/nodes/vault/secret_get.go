package vault

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/secrets"
	vaultstore "github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// lookupSecretIDByName resolves a vault entry name to its internal id,
// scoped to profileID — mirrors cmd/monoagentcli/secret.go's lookupSecretID,
// which the CLI's own `secret reveal/update/rm` commands use for the same
// name-to-id translation (users address entries by name; storage is by id).
func lookupSecretIDByName(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	entries, err := secrets.List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("looking up secret: %w", err)
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, nil
		}
	}
	return "", fmt.Errorf("no secret named %q found", name)
}

// SecretGetNode decrypts a vault entry and puts its fields into the item
// stream so downstream nodes can use them (e.g. an HTTP node's Authorization
// header, or a browser-automation node's login form). This intentionally
// exposes plaintext into the item stream — see the node's ref.go doc for the
// exposure this carries (execution history, downstream nodes, no automatic
// masking beyond the ~16 known key names internal/workflow/redact.go
// recognizes at the MCP/REST/chat-tool display boundaries only).
type SecretGetNode struct{}

func (n *SecretGetNode) Type() string { return "vault.secret_get" }

// LiteralConfigFields: name is the vault entry it reads
// (workflow.LiteralConfigResolver).
func (n *SecretGetNode) LiteralConfigFields() []string { return []string{"name"} }

func (n *SecretGetNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	// What the chat, an agent or another workflow wrote reads no secret until
	// the user approves it (secrets.RefuseResolving, as for every @secret
	// reference): its next step could send the value anywhere, and the run's
	// output shows it to whoever started it.
	if why := secrets.ResolveRefusal(ctx); why != "" {
		return nil, fmt.Errorf("vault.secret_get decrypts a vault entry, which this run may not: %s", why)
	}
	name, _ := config["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("vault.secret_get: name is required")
	}
	outputField, _ := config["output_field"].(string)
	if outputField == "" {
		outputField = "credential"
	}

	db := vaultstore.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("vault.secret_get: no database in context")
	}
	profileID, perr := vaultstore.ProfileIDFromContext(ctx)
	if perr != nil {
		return nil, fmt.Errorf("vault.secret_get: ran without a profile: %w", perr)
	}

	id, err := lookupSecretIDByName(ctx, db, profileID, name)
	if err != nil {
		return nil, fmt.Errorf("vault.secret_get: %w", err)
	}
	fields, _, err := secrets.DecryptFields(ctx, db, profileID, id)
	if err != nil {
		return nil, fmt.Errorf("vault.secret_get: %w", err)
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		credential := make(map[string]interface{}, len(fields))
		for k, v := range fields {
			credential[k] = v
		}
		newJSON[outputField] = credential
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}
