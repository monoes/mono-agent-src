package secrets

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"

	"github.com/zalando/go-keyring"
)

func newExportTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db := testdb.Open(t)
	return db
}

func findEntryID(entries []Entry, name string) string {
	for _, e := range entries {
		if e.Name == name {
			return e.ID
		}
	}
	return ""
}

func TestGenerateExportPassword_IsNonEmptyAndVaries(t *testing.T) {
	a, err := GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	b, err := GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty generated values")
	}
	if a == b {
		t.Fatal("expected two independently generated values to differ")
	}
}

func TestExportImport_RoundTrip(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", "note text"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := Add(ctx, db.DB, "default", "login", "e2", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	exportPW := "pw-correct1"
	data, exported, skipped, err := Export(ctx, db.DB, "default", exportPW)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported != 2 || skipped != 0 {
		t.Fatalf("expected 2 exported, 0 skipped, got exported=%d skipped=%d", exported, skipped)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
	if strings.Contains(string(data), "v-alpha1") || strings.Contains(string(data), "p-one1") {
		t.Fatal("export file must not contain plaintext")
	}

	db2 := newExportTestDB(t)
	imported, skipped, err := Import(context.Background(), db2.DB, "default", exportPW, data, nil, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 2 || skipped != 0 {
		t.Fatalf("expected 2 imported, 0 skipped, got imported=%d skipped=%d", imported, skipped)
	}

	entries, err := List(context.Background(), db2.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after import, got %d", len(entries))
	}

	fields, _, err := DecryptFields(context.Background(), db2.DB, "default", findEntryID(entries, "e1"))
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "v-alpha1" {
		t.Fatalf("got %q, want %q", fields["secret"], "v-alpha1")
	}
}

// TestExport_SkipsEntryThatFailsToDecrypt covers an unmigrated legacy row
// (see insertLegacyRow in migrate_kv_test.go) sitting alongside normal
// entries: its ciphertext decrypts fine as raw bytes but isn't valid JSON,
// so DecryptFields fails on it specifically. Export must skip just that
// entry — not abort the whole export — and report accurate exported/skipped
// counts, with the bad entry excluded from the resulting payload.
func TestExport_SkipsEntryThatFailsToDecrypt(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "good-one", map[string]string{"secret": "v-good1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	insertLegacyRow(t, db, "sec-legacy", "secret", "legacy-broken", "not-json-plaintext")
	if _, err := Add(ctx, db.DB, "default", "secret", "good-two", map[string]string{"secret": "v-good2"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	data, exported, skipped, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported != 2 || skipped != 1 {
		t.Fatalf("expected 2 exported, 1 skipped, got exported=%d skipped=%d", exported, skipped)
	}

	// The payload itself must only contain the two decryptable entries.
	db2 := newExportTestDB(t)
	imported, importSkipped, err := Import(context.Background(), db2.DB, "default", "pw-correct1", data, nil, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 2 || importSkipped != 0 {
		t.Fatalf("expected 2 imported into a fresh vault, 0 skipped, got imported=%d skipped=%d", imported, importSkipped)
	}
	entries, err := List(context.Background(), db2.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after import, got %d", len(entries))
	}
	if findEntryID(entries, "legacy-broken") != "" {
		t.Fatal("expected the undecryptable legacy entry to be excluded from the export")
	}
}

func TestImport_WrongPassphraseFails(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, _, _, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	db2 := newExportTestDB(t)
	if _, _, err := Import(ctx, db2.DB, "default", "pw-incorrect1", data, nil, nil); err == nil {
		t.Fatal("expected import with an incorrect passphrase to fail")
	}
}

func TestImport_SkipsDuplicateNames(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "shared", map[string]string{"secret": "v-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, _, _, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import into the SAME vault — "shared" already exists there.
	imported, skipped, err := Import(ctx, db.DB, "default", "pw-correct1", data, nil, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 0 || skipped != 1 {
		t.Fatalf("expected 0 imported, 1 skipped, got imported=%d skipped=%d", imported, skipped)
	}
}

func TestImport_RejectsUnrecognizedFormat(t *testing.T) {
	db := newExportTestDB(t)
	if _, _, err := Import(context.Background(), db.DB, "default", "any-passphrase", []byte(`{"format":"something-else","version":1}`), nil, nil); err == nil {
		t.Fatal("expected error for an unrecognized export format, got nil")
	}
}

func TestExportImport_SystemEntryRoundTripsWithMetaAndRematerializes(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	// connections comes from migration 095.

	token := "PLACEHOLDER-one"
	vaultID, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub — work",
		map[string]string{"access_token": token}, "acct-1", "")
	if err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	if _, err := db.DB.Exec(
		`INSERT INTO connections (id, platform, method, label, account_id, profile_id, vault_ref, created_at, updated_at) VALUES ('conn-1', 'github', 'oauth', 'work', 'acct-1', 'default', ?, 't', 't')`,
		vaultID); err != nil {
		t.Fatalf("seeding connections row: %v", err)
	}

	passphrase := "pw-123"
	data, exported, skipped, err := Export(ctx, db.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported != 1 || skipped != 0 {
		t.Fatalf("expected exported=1 skipped=0, got exported=%d skipped=%d", exported, skipped)
	}

	// Simulate a fresh machine: a brand-new db with no connections row at all.
	dst := newExportTestDB(t)
	// connections comes from migration 095.

	var rematerializedMeta map[string]string
	rematerializeConnection := func(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
		rematerializedMeta = meta
		_, err := db.ExecContext(ctx,
			`INSERT INTO connections (id, platform, method, label, account_id, profile_id, vault_ref, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 't', 't')`,
			"conn-imported", meta["platform"], meta["method"], meta["label"], meta["account_id"], profileID, vaultID)
		return err
	}

	imported, importSkipped, err := Import(ctx, dst.DB, "default", passphrase, data, rematerializeConnection, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 || importSkipped != 0 {
		t.Fatalf("expected imported=1 skipped=0, got imported=%d skipped=%d", imported, importSkipped)
	}
	if rematerializedMeta["platform"] != "github" || rematerializedMeta["label"] != "work" || rematerializedMeta["account_id"] != "acct-1" {
		t.Fatalf("unexpected rematerialize meta: %+v", rematerializedMeta)
	}

	var count int
	if err := dst.DB.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = 'conn-imported'`).Scan(&count); err != nil {
		t.Fatalf("counting imported connections: %v", err)
	}
	if count != 1 {
		t.Fatal("expected the connection row to be rematerialized on import")
	}

	entries, err := List(ctx, dst.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != "connection" || entries[0].Name != "GitHub — work" {
		t.Fatalf("unexpected imported vault entries: %+v", entries)
	}
}

func TestImport_NilRematerializerSkipsGracefully(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	// connections comes from migration 095.
	token := "PLACEHOLDER-a"
	if _, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": token}, "", ""); err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	passphrase := "pw-123"
	data, _, _, err := Export(ctx, db.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newExportTestDB(t)
	// nil rematerializeConnection: the vault entry still imports, no panic.
	imported, _, err := Import(ctx, dst.DB, "default", passphrase, data, nil, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 {
		t.Fatalf("expected the vault entry to import even with a nil rematerializer, got imported=%d", imported)
	}
}

// TestImport_RejectsBadVersionOrKDF verifies Import refuses envelopes that
// don't claim the exact version/KDF this package decrypts with, instead of
// deriving a key with the wrong parameters and failing confusingly.
func TestImport_RejectsBadVersionOrKDF(t *testing.T) {
	db := newExportTestDB(t)

	// Same format, but version 2.
	if _, _, err := Import(context.Background(), db.DB, "default", "pw", []byte(`{"format":"monoagent-vault-export","version":2,"kdf":"argon2id"}`), nil, nil); err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	// Same format, but a different KDF.
	if _, _, err := Import(context.Background(), db.DB, "default", "pw", []byte(`{"format":"monoagent-vault-export","version":1,"kdf":"scrypt"}`), nil, nil); err == nil {
		t.Fatal("expected error for unsupported KDF, got nil")
	}
}
