package vault

import (
	"context"
	"database/sql"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the transaction
// (#1373): the next user of the pool would fail with "cannot start a transaction within a transaction". The
// tables below are only what each function reads before it begins.
func TestCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(Wait)
	src := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithProfileID(context.Background(), "p")
	type handle struct{ DB *sql.DB }
	type call func(t *testing.T, db *handle) error
	tests := map[string]call{
		"Register": func(t *testing.T, db *handle) error {
			_, err := Register(ctx, db.DB, src, "upload", "", "")
			return err
		},
		"SaveBytes": func(t *testing.T, db *handle) error {
			_, err := SaveBytes(ctx, db.DB, "p", SaveInput{Data: []byte("x"), Ext: ".png", ClientID: "c", Source: "s"})
			return err
		},
		"registerInPlace": func(t *testing.T, db *handle) error {
			_, err := registerInPlace(ctx, db.DB, "p", src, "upload", "", "")
			return err
		},
		"RegisterDiscoveredImage": func(t *testing.T, db *handle) error {
			_, _, err := RegisterDiscoveredImage(ctx, db.DB, "p", src, "a.png", 1)
			return err
		},
		"RegisterDocument": func(t *testing.T, db *handle) error {
			_, err := RegisterDocument(ctx, db.DB, src, "upload")
			return err
		},
		"RegisterDiscoveredDocument": func(t *testing.T, db *handle) error {
			_, _, err := RegisterDiscoveredDocument(ctx, db.DB, "p", src, "a.png", 1)
			return err
		},
		"registerCaptureDocument": func(t *testing.T, db *handle) error {
			_, _, err := registerCaptureDocument(ctx, db.DB, "p", CaptureDocument{Dir: "d", Path: src}, "n")
			return err
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			testhome.SetHome(t, t.TempDir())
			db, trip := dbconntest.Open(t)
			h := &handle{DB: db}
			for _, ddl := range []string{
				`CREATE TABLE vault_images (id TEXT, profile_id TEXT, path TEXT)`,
				`CREATE TABLE vault_documents (id TEXT, profile_id TEXT, path TEXT)`,
			} {
				if _, err := db.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			trip.Arm()
			if err := run(t, h); !errors.Is(err, context.Canceled) {
				t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
			}
			dbconntest.RequireClean(t, db)
		})
	}
}
