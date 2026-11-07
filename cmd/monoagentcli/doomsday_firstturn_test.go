package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// `status`'s first-turn read opens the database read-only: a missing file stays missing and a database older than the mode is
// byte-for-byte what it was (no creation, no migration, no vault migration). Mutant: opening with initDB (red on both).
func TestFirstTurnReadCreatesAndMigratesNothing(t *testing.T) {
	cfg := sendEnv(t)
	cfg.ProfileID = "p1"
	cfg.DBPath = filepath.Join(t.TempDir(), ".monoagent", "monoagent.db")
	st, err := firstTurnFor(cfg)(context.Background())
	if err != nil || st.State != "" {
		t.Fatalf("missing database: %+v %v", st, err)
	}
	if _, err := os.Stat(cfg.DBPath); !os.IsNotExist(err) {
		t.Fatalf("the read created the database: %v", err)
	}
	cfg.DBPath = filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	sum := func() [32]byte {
		b, err := os.ReadFile(cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(b)
	}
	before := sum()
	if st, err := firstTurnFor(cfg)(context.Background()); err != nil || st.State != "" {
		t.Fatalf("old database: %+v %v", st, err)
	}
	if sum() != before {
		t.Fatal("the read changed an older database")
	}
}
