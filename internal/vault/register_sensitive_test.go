package vault_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
)

// Register and RegisterDocument must not copy a credential file into the vault.
func TestRegisterRefusesCredentialLocations(t *testing.T) {
	db := newTestDB(t)
	home := os.Getenv("HOME")
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	for _, p := range []string{key, link} {
		if _, err := vault.RegisterDocument(ctx, db.DB, p, "upload"); err == nil {
			t.Errorf("RegisterDocument(%s) accepted a credential file", p)
		}
		if _, err := vault.Register(ctx, db.DB, p, "upload", "", ""); err == nil {
			t.Errorf("Register(%s) accepted a credential file", p)
		}
	}
}
