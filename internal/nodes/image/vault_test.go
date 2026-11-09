package image

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newImageVaultTestCtx builds a real migrated DB and, crucially, redirects
// $HOME to a throwaway temp dir first — vault.VaultDir resolves under
// os.UserHomeDir() by default, and without this override the test would
// write into the real user's ~/.monoagent/profiles/default/vault/.
func newImageVaultTestCtx(t *testing.T) context.Context {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	// The vault syncs each file into the knowledge graph in the background:
	// run a no-op instead of the real monomind, and let it finish before HOME
	// is removed.
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)

	dbPath := filepath.Join(t.TempDir(), "vault-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })

	ctx := vault.ContextWithDB(context.Background(), db.DB)
	ctx = vault.ContextWithProfileID(ctx, "default")
	return ctx
}

func writeTestImage(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	return p
}

func TestImageVaultSaveGet_RoundTrip(t *testing.T) {
	ctx := newImageVaultTestCtx(t)
	srcPath := writeTestImage(t, "fake-png-bytes")

	save := &ImageVaultSaveNode{}
	input := workflow.NodeInput{
		WorkflowID:  "wf1",
		ExecutionID: "ex1",
		Items:       []workflow.Item{workflow.NewItem(map[string]interface{}{"image_path": srcPath})},
	}
	outputs, err := save.Execute(ctx, input, map[string]interface{}{"source": "test"})
	if err != nil {
		t.Fatalf("ImageVaultSaveNode.Execute: %v", err)
	}
	vaultID, _ := outputs[0].Items[0].JSON["vault_id"].(string)
	if vaultID == "" {
		t.Fatalf("expected a vault_id, got %+v", outputs[0].Items[0].JSON)
	}

	get := &ImageVaultGetNode{}
	getInput := workflow.NodeInput{
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"vault_id": vaultID})},
	}
	outputs, err = get.Execute(ctx, getInput, map[string]interface{}{})
	if err != nil {
		t.Fatalf("ImageVaultGetNode.Execute: %v", err)
	}
	resolvedPath, _ := outputs[0].Items[0].JSON["image_path"].(string)
	if resolvedPath == "" {
		t.Fatalf("expected a resolved image_path, got %+v", outputs[0].Items[0].JSON)
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		t.Fatalf("reading resolved vault file: %v", err)
	}
	if string(data) != "fake-png-bytes" {
		t.Fatalf("vault file content = %q, want %q", data, "fake-png-bytes")
	}
	// The vault copy must be independent of the original source file.
	if resolvedPath == srcPath {
		t.Fatalf("resolved path should be the vault's own copy, not the original source path")
	}
}

func TestImageVaultGet_AcceptsIdWithOrWithoutAtPrefix(t *testing.T) {
	ctx := newImageVaultTestCtx(t)
	srcPath := writeTestImage(t, "x")
	save := &ImageVaultSaveNode{}
	outputs, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"image_path": srcPath})}},
		map[string]interface{}{})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	vaultID, _ := outputs[0].Items[0].JSON["vault_id"].(string)

	get := &ImageVaultGetNode{}
	for _, ref := range []string{vaultID, "@" + vaultID} {
		outputs, err := get.Execute(ctx,
			workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"vault_id": ref})}},
			map[string]interface{}{})
		if err != nil {
			t.Fatalf("get(%q): %v", ref, err)
		}
		if outputs[0].Items[0].JSON["image_path"] == "" {
			t.Fatalf("get(%q): empty image_path", ref)
		}
	}
}

func TestImageVaultSave_RejectsMissingImagePath(t *testing.T) {
	ctx := newImageVaultTestCtx(t)
	save := &ImageVaultSaveNode{}
	_, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{})}},
		map[string]interface{}{})
	if err == nil {
		t.Fatalf("expected an error when the item has no image path")
	}
}

func TestImageVaultGet_RejectsUnknownId(t *testing.T) {
	ctx := newImageVaultTestCtx(t)
	get := &ImageVaultGetNode{}
	_, err := get.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"vault_id": "img-999"})}},
		map[string]interface{}{})
	if err == nil {
		t.Fatalf("expected an error for an unknown vault id")
	}
}

// TestImageVaultGet_RejectsMissingVaultFile covers a real-world case
// vault.Resolve doesn't itself guard against: the DB row exists and points
// at a path, but the file itself was deleted from disk (manual cleanup,
// a moved profile directory, etc). Without this check, ImageVaultGetNode
// would hand a dead path to whatever node runs next, which would then fail
// with a much more confusing "open <path>: no such file" error far removed
// from the actual cause.
func TestImageVaultGet_RejectsMissingVaultFile(t *testing.T) {
	ctx := newImageVaultTestCtx(t)
	srcPath := writeTestImage(t, "x")
	save := &ImageVaultSaveNode{}
	outputs, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"image_path": srcPath})}},
		map[string]interface{}{})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	vaultID, _ := outputs[0].Items[0].JSON["vault_id"].(string)

	// Resolve once to find where the vault copy actually landed, then
	// delete it — simulating the file having gone missing independently of
	// the DB row.
	db := vault.DBFromContext(ctx)
	vaultPath, err := vault.Resolve(ctx, db, "@"+vaultID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.Remove(vaultPath); err != nil {
		t.Fatalf("removing vault file to simulate loss: %v", err)
	}

	get := &ImageVaultGetNode{}
	_, err = get.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"vault_id": vaultID})}},
		map[string]interface{}{})
	if err == nil {
		t.Fatalf("expected an error for a vault entry whose file is missing on disk")
	}
}

// TestImageVaultSave_BatchGivesEachItemItsOwnId covers multi-item execution
// — every node in this package processes input.Items in a loop, but only
// single-item behavior had test coverage before this.
func TestImageVaultSave_BatchGivesEachItemItsOwnId(t *testing.T) {
	ctx := newImageVaultTestCtx(t)
	pathA := writeTestImage(t, "content-a")
	pathB := writeTestImage(t, "content-b")

	save := &ImageVaultSaveNode{}
	outputs, err := save.Execute(ctx,
		workflow.NodeInput{Items: []workflow.Item{
			workflow.NewItem(map[string]interface{}{"image_path": pathA}),
			workflow.NewItem(map[string]interface{}{"image_path": pathB}),
		}},
		map[string]interface{}{})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(outputs[0].Items) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(outputs[0].Items))
	}
	idA, _ := outputs[0].Items[0].JSON["vault_id"].(string)
	idB, _ := outputs[0].Items[1].JSON["vault_id"].(string)
	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("expected two distinct non-empty vault ids, got %q and %q", idA, idB)
	}
}
