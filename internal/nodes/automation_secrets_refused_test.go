package nodes

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A {{secret:<name>}} step of an automation reads the vault through the same
// check as an @secret reference: in a run the user hasn't approved it finds
// nothing, so the value never reaches the page; in the user's run it does
// (#845 review, item 1).
func TestSecretTemplateResolvesOnlyInTheUsersRun(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "automation:acme/api_key", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok := SecretLookup(workflow.WithAIAuthored(ctx), db.DB, "default", "acme")("api_key"); ok || v != "" {
		t.Errorf("an unapproved run's {{secret:api_key}} = %q, %v", v, ok)
	}
	if v, ok := SecretLookup(ctx, db.DB, "default", "acme")("api_key"); !ok || v != "vsecret-777" {
		t.Errorf("the user's run's {{secret:api_key}} = %q, %v", v, ok)
	}
}
