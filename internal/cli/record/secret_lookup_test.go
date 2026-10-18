package record

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// `record verify` and `record replay` type a recording's {{secret:}} values
// into a page: run from an agent's shell they read no secret, since the
// recording and the page may be the agent's; the user's run reads them.
func TestRecordSecretLookupIsTheUsersOnly(t *testing.T) {
	keyring.MockInit()
	t.Setenv("HOME", t.TempDir())
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), db.DB, "default", "secret", "automation:example-go/pw", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	db.Close()
	d := testDeps(false, dbPath)
	d.ProfileID = func() string { return "default" }
	lookup := func() (string, bool) {
		l, release := d.VaultSecretLookup(context.Background(), "example-go")
		defer release()
		return l("pw")
	}
	t.Setenv("CLAUDECODE", "1")
	if v, ok := lookup(); ok || v != "" {
		t.Errorf("an agent's replay read the secret: %q", v)
	}
	t.Setenv("CLAUDECODE", "")
	if v, ok := lookup(); !ok || v != "vsecret-777" {
		t.Errorf("the user's replay: %q, %v", v, ok)
	}
}
