package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/storage"
)

// `db migrate` takes an empty HOME to a migrated database with every
// profile's folder laid out (a new database has none), says what it did, and
// is a no-op the second time.
func TestDBMigrateJSON(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	home := t.TempDir()

	run := func() dbMigrateResult {
		t.Helper()
		// Named with no profile, as the desktop app runs it: a new database has none to name.
		out, errOut, err := runCLI(t, home, "--profile=", "--json", "db", "migrate")
		if err != nil {
			t.Fatalf("db migrate: %v\nstderr:\n%s", err, errOut)
		}
		var res dbMigrateResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("stdout is not the result: %v\n%s", err, out)
		}
		return res
	}

	first := run()
	if first.Applied == 0 || first.SchemaVersion == 0 {
		t.Fatalf("first run applied nothing: %+v", first)
	}
	if len(first.Profiles) != 0 {
		t.Fatalf("profiles on a new database = %+v, want none", first.Profiles)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("errors: %v", first.Errors)
	}
	if fi, err := os.Stat(filepath.Join(home, ".monoagent", "monoagent.db")); err != nil || fi.Size() == 0 {
		t.Fatalf("database not created: %v", err)
	}
	// The app migrates through this command (it never opens the database
	// to write), so it also runs the one-time approvals backfill.
	db, err := storage.NewDatabase(filepath.Join(home, ".monoagent", "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	var backfilled int
	err = db.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'workflow_approvals.backfilled'`).Scan(&backfilled)
	db.Close()
	if err != nil || backfilled != 1 {
		t.Fatalf("db migrate did not run the approvals backfill: %v, %d", err, backfilled)
	}

	second := run()
	if second.Applied != 0 || second.SchemaVersion != first.SchemaVersion || second.VaultMoved != 0 {
		t.Fatalf("second run was not a no-op: %+v (first %+v)", second, first)
	}
}

// The desktop app runs `db migrate` before anyone signs in; the gate decides
// whether it may, like every other command that opens the database.
func TestDBMigrateIsGated(t *testing.T) {
	root := newRootCmd()
	applyClassification(root)
	c, _, err := root.Find([]string{"db", "migrate"})
	if err != nil || commandClass(c) != classGated {
		t.Fatalf("db migrate: class %q, err %v", commandClass(c), err)
	}
}
