package httpnodes

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

// http.request resolves its per-item fields (the url, the body, …) itself,
// against each item, and checks them against the fields as the workflow
// holds them: an item's "@secret:vs" or "@img-1" in the body is refused,
// and the workflow's own reference there resolves (#845 code review, 3).
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
	engine := workflow.NewExpressionEngine()
	item := workflow.NewItem(map[string]interface{}{"msg": "@secret:vs"})

	cfg, err := resolveItemFields(ctx, engine, map[string]interface{}{"body": "{{ $json.msg }}"}, requestPerItemFields, item, workflow.NodeInput{}, "http.request")
	if !errors.Is(err, secrets.ErrDataReference) || cfg != nil {
		t.Errorf("an item's reference in the body: %v, %v", cfg, err)
	}
	cfg, err = resolveItemFields(ctx, engine, map[string]interface{}{"body": "@secret:vs"}, requestPerItemFields, item, workflow.NodeInput{}, "http.request")
	if err != nil || cfg["body"] != "vsecret-777" {
		t.Errorf("the workflow's own reference in the body: %v, %v", cfg, err)
	}

	// The same for an @img reference, which resolves to a vault file.
	img := workflow.NewItem(map[string]interface{}{"msg": "@img-1"})
	cfg, err = resolveItemFields(ctx, engine, map[string]interface{}{"body": "{{ $json.msg }}"}, requestPerItemFields, img, workflow.NodeInput{}, "http.request")
	if !errors.Is(err, vault.ErrDataReference) || cfg != nil {
		t.Errorf("an item's image reference in the body: %v, %v", cfg, err)
	}
	cfg, err = resolveItemFields(ctx, engine, map[string]interface{}{"body": "@img-1"}, requestPerItemFields, img, workflow.NodeInput{}, "http.request")
	if err != nil || cfg["body"] != "/vault/passport.png" {
		t.Errorf("the workflow's own image reference in the body: %v, %v", cfg, err)
	}
}
