package storage_test

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// A one-shot command registers a vault image and closes its database: Close
// must let the image's knowledge-graph sync land, as it does for its own
// upserts, or the node is lost when the process exits.
func TestCloseLetsVaultImageSyncLand(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	scratch := t.TempDir()
	marker := filepath.Join(scratch, "synced")
	bin := filepath.Join(scratch, "monomind")
	script := "#!/bin/sh\nsleep 0.4\n: > " + marker + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOMIND_BIN", bin)
	t.Cleanup(vault.Wait)

	db, err := storage.NewDatabase(filepath.Join(scratch, "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(scratch, "a.png")
	if err := os.WriteFile(src, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(context.Background(), "default")
	if _, err := vault.Register(ctx, db.DB, src, "test", "", ""); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Close returned before the vault image's sync finished: %v", err)
	}
}
