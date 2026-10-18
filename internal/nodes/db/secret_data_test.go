package dbnodes

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A database node resolves its per-item fields (the query, …) itself,
// against each item, and checks them against the fields as the workflow
// holds them: an item's "@secret:vs" or "@img-1" is refused, and the
// workflow's own reference resolves (#845 code review, 3).
func TestAPerItemFieldTakesNoReferenceFromAnItem(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO vault_images (id, seq, path, filename, profile_id) VALUES ('img-1', 1, '/vault/passport.png', 'passport.png', 'default')`); err != nil {
		t.Fatal(err)
	}
	item := workflow.NewItem(map[string]interface{}{"q": "@secret:vs"})

	cfg, err := resolveForItem(ctx, map[string]interface{}{"query": "{{ $json.q }}"}, []string{"query"}, item, workflow.NodeInput{})
	if !errors.Is(err, secrets.ErrDataReference) || cfg != nil {
		t.Errorf("an item's reference in the query: %v, %v", cfg, err)
	}
	cfg, err = resolveForItem(ctx, map[string]interface{}{"query": "@secret:vs"}, []string{"query"}, item, workflow.NodeInput{})
	if err != nil || cfg["query"] != "vsecret-777" {
		t.Errorf("the workflow's own reference in the query: %v, %v", cfg, err)
	}

	// The same for an @img reference, which resolves to a vault file.
	img := workflow.NewItem(map[string]interface{}{"q": "@img-1"})
	cfg, err = resolveForItem(ctx, map[string]interface{}{"query": "{{ $json.q }}"}, []string{"query"}, img, workflow.NodeInput{})
	if !errors.Is(err, vault.ErrDataReference) || cfg != nil {
		t.Errorf("an item's image reference in the query: %v, %v", cfg, err)
	}
	cfg, err = resolveForItem(ctx, map[string]interface{}{"query": "@img-1"}, []string{"query"}, img, workflow.NodeInput{})
	if err != nil || cfg["query"] != "/vault/passport.png" {
		t.Errorf("the workflow's own image reference in the query: %v, %v", cfg, err)
	}
}
