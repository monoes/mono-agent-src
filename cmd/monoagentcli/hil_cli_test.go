package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	hilcli "github.com/monoes/mono-agent/internal/cli/hil"
	"github.com/monoes/mono-agent/internal/storage"
)

// The hil commands' own tests live in internal/cli/hil. hil_send_test.go stays here: it drives
// the send path end to end through this adapter (node run queues, requireOperator refuses an
// agent, hilSendRunner runs, publication register checks the approval). newHILTestDB stays for
// it and the jev tests, and runHIL for it.

// migratedTemplate is a migrated, checkpointed and closed database file, built once per test binary
// (under TestMain's keyring mock and test HOME, like any other caller). Migrating a fresh database is
// about 95% of the cost of a table row under -race (4-6 s); copying the bytes is a few milliseconds.
// Migrations insert only constant rows (the default profile, settings, generation counters) and
// nothing random, so a copy is the same database a fresh migration would give. A test that needs the
// migration itself to run (a migration test, a database that must be migrated at open) migrates a
// fresh file with storage.NewDatabase + ApplyMigrations instead of using the template.
var (
	migratedTemplateOnce  sync.Once
	migratedTemplateBytes []byte
	migratedTemplateErr   error
)

func migratedTemplate() ([]byte, error) {
	migratedTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "migrated-template-")
		if err != nil {
			migratedTemplateErr = err
			return
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "template.db")
		db, err := storage.NewDatabase(p)
		if err != nil {
			migratedTemplateErr = err
			return
		}
		if err = db.ApplyMigrations(); err == nil {
			// Fold the WAL into the file so the bytes alone are the database.
			_, err = db.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		}
		if cerr := db.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			for _, side := range []string{"-wal", "-shm"} {
				if fi, serr := os.Stat(p + side); serr == nil && fi.Size() > 0 {
					err = fmt.Errorf("template database left a non-empty %s file", side)
				}
			}
		}
		if err != nil {
			migratedTemplateErr = err
			return
		}
		migratedTemplateBytes, migratedTemplateErr = os.ReadFile(p)
	})
	return migratedTemplateBytes, migratedTemplateErr
}

// writeMigratedDB puts a migrated database at path (a copy of the template).
func writeMigratedDB(t *testing.T, path string) {
	t.Helper()
	b, err := migratedTemplate()
	if err != nil {
		t.Fatalf("migrated template database: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newHILTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hil-test.db")
	writeMigratedDB(t, dbPath)
	return dbPath
}

func runHIL(t *testing.T, cfg *globalConfig, args ...string) ([]hilcli.Item, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := newHILCmd(cfg)
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	if err != nil || args[0] != "list" {
		return nil, err
	}
	var items []hilcli.Item
	if jerr := json.Unmarshal([]byte(out), &items); jerr != nil {
		t.Fatalf("list output %q: %v", out, jerr)
	}
	return items, nil
}
