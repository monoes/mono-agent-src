package secrets

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// beginDB is a pool over the cancel-after-BEGIN driver with the tables the code under test reads before it begins:
// a per-profile vault_keys, vault_keys_legacy with one legacy key, and vault_secrets with one row of the profile.
func beginDB(t *testing.T) (*sql.DB, *dbconntest.Trip) {
	t.Helper()
	db, trip := dbconntest.Open(t)
	for _, ddl := range []string{
		`CREATE TABLE vault_keys (profile_id TEXT PRIMARY KEY, wrapped_dek BLOB NOT NULL, wrapped_nonce BLOB NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE vault_keys_legacy (id INTEGER PRIMARY KEY, wrapped_dek BLOB NOT NULL, wrapped_nonce BLOB NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE vault_secrets (id TEXT, profile_id TEXT)`,
		`INSERT INTO vault_secrets (id, profile_id) VALUES ('sec-001', 'p')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	if err := keyring.Set(keyringService, legacyKeyringAccount, hex.EncodeToString(kek)); err != nil {
		t.Fatal(err)
	}
	wrapped, nonce, err := Encrypt(kek, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO vault_keys_legacy (id, wrapped_dek, wrapped_nonce, created_at) VALUES (1, ?, ?, 'now')`, wrapped, nonce); err != nil {
		t.Fatal(err)
	}
	return db, trip
}

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the transaction
// (#1373): the next user of the pool would fail with "cannot start a transaction within a transaction".
func TestCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	ctx := context.Background()
	steps := map[string]func(db *sql.DB) error{
		"addEntry": func(db *sql.DB) error {
			_, err := addEntry(ctx, db, "p", "secret", "n", map[string]string{"k": "v"}, "", "", "")
			return err
		},
		"bootstrapDEKLocked": func(db *sql.DB) error {
			_, err := bootstrapDEKLocked(ctx, db, "q")
			return err
		},
		"MigrateProfileVaultKeys": func(db *sql.DB) error {
			_, errs := MigrateProfileVaultKeys(ctx, db, "p")
			if len(errs) != 1 {
				t.Fatalf("errors = %v, want the begin's", errs)
			}
			return errs[0]
		},
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			db, trip := beginDB(t)
			// The profile's key exists before the BEGIN under test, so that BEGIN is the one that is armed. Making it
			// reads through the pool while it holds a connection, which needs a second one; the test then goes back
			// to the one connection RequireClean checks.
			db.SetMaxOpenConns(2)
			if _, err := getOrCreateDEK(ctx, db, "p"); err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			trip.Arm()
			if err := step(db); !errors.Is(err, context.Canceled) {
				t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
			}
			dbconntest.RequireClean(t, db)
		})
	}
}
