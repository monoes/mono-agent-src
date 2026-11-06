package secrets

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"

	"github.com/zalando/go-keyring"
)

func newMigrateKVTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db := testdb.Open(t)
	return db
}

// insertLegacyRow writes a vault_secrets row the pre-key-value way (a single
// encrypted string, kv defaulting to 0), bypassing Add — which, once Task 2
// lands, can only ever produce kv=1 rows. This simulates data left over from
// before this migration shipped.
func insertLegacyRow(t *testing.T, db *storage.Database, id, kind, name, plainValue string) {
	t.Helper()
	ctx := context.Background()
	dek, err := getOrCreateDEK(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("getOrCreateDEK: %v", err)
	}
	ciphertext, nonce, err := Encrypt(dek, []byte(plainValue))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.DB.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, seq, profile_id, kind, name, ciphertext, nonce, created_at, updated_at)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_secrets), 'default', ?, ?, ?, ?, ?, ?)`,
		id, kind, name, ciphertext, nonce, now, now,
	)
	if err != nil {
		t.Fatalf("inserting legacy row: %v", err)
	}
}

func TestMigrateFieldsToKV_NoOpWhenNothingToMigrate(t *testing.T) {
	db := newMigrateKVTestDB(t)
	migrated, total, err := MigrateFieldsToKV(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("MigrateFieldsToKV: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected no-op on empty vault, got migrated=%d total=%d", migrated, total)
	}
}

func TestMigrateFieldsToKV_MigratesLegacyRow(t *testing.T) {
	db := newMigrateKVTestDB(t)
	insertLegacyRow(t, db, "sec-001", "secret", "svc-one", "v-legacy1")
	insertLegacyRow(t, db, "sec-002", "login", "svc-two", "p-one1")

	migrated, total, err := MigrateFieldsToKV(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("MigrateFieldsToKV: %v", err)
	}
	if migrated != 2 || total != 2 {
		t.Fatalf("expected 2 migrated of 2 total, got migrated=%d total=%d", migrated, total)
	}

	want := map[string]string{"sec-001": "v-legacy1", "sec-002": "p-one1"}
	for id, wantValue := range want {
		fields, _, err := DecryptFields(context.Background(), db.DB, "default", id)
		if err != nil {
			t.Fatalf("DecryptFields(%s): %v", id, err)
		}
		if fields["secret"] != wantValue {
			t.Fatalf("%s: got fields[secret]=%q, want %q", id, fields["secret"], wantValue)
		}
		if len(fields) != 1 {
			t.Fatalf("%s: expected exactly 1 field, got %d", id, len(fields))
		}
	}

	entries, err := List(context.Background(), db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.FieldCount != 1 {
			t.Fatalf("%s: expected field_count=1 after migration, got %d", e.ID, e.FieldCount)
		}
	}
}

func TestMigrateFieldsToKV_IsIdempotent(t *testing.T) {
	db := newMigrateKVTestDB(t)
	insertLegacyRow(t, db, "sec-001", "secret", "svc-one", "v-legacy1")

	if _, _, err := MigrateFieldsToKV(context.Background(), db.DB); err != nil {
		t.Fatalf("first MigrateFieldsToKV: %v", err)
	}
	migrated, total, err := MigrateFieldsToKV(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("second MigrateFieldsToKV: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected second run to be a no-op, got migrated=%d total=%d", migrated, total)
	}
}
