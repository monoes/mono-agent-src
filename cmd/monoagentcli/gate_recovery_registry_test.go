package main

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
	_ "modernc.org/sqlite"
)

// registerBoth registers, in a registry of the test's own, an unreadable database that sorts FIRST
// (a-junk.db, a live host) and, when withTurn is set, a readable database z-turn.db that records this
// process as a coder turn. The command names a third database that holds no turn.
func registerBoth(t *testing.T, withTurn bool) {
	t.Helper()
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	cfg := opEnv(t)
	dir := t.TempDir()
	junk := filepath.Join(dir, "a-junk.db")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a database ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{junk}
	if withTurn {
		turn := filepath.Join(dir, "z-turn.db")
		seedMigratedDB(t, turn)
		markThisProcessAsACoderTurn(t, &globalConfig{DBPath: turn})
		paths = append(paths, turn)
	}
	for _, p := range paths {
		unregister, err := coderhost.RegisterHostDB(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unregister)
	}
	cfg.DBPath = newHILTestDB(t)
}

// seedMigratedDB creates a fully migrated database at path.
func seedMigratedDB(t *testing.T, path string) {
	t.Helper()
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
}

// An unreadable registry entry must not hide a turn recorded in a readable one: the recovery flavour skips
// the unreadable entry and still refuses; the other flavour refuses at the unreadable entry, as before.
func TestARecoveryVerbIsRefusedForATurnRecordedPastAnUnreadableRegistryEntry(t *testing.T) {
	registerBoth(t, true)
	if why := opgate.RecoveryRefusal(); !strings.Contains(why, "coder session's turn") {
		t.Fatalf("recovery let a turn through because an unreadable entry sorts first: %q", why)
	}
	if why := opgate.Refusal(); !strings.Contains(why, "cannot tell") || !strings.Contains(why, "a-junk.db") {
		t.Fatalf("the non-recovery gate must still refuse at the unreadable entry: %q", why)
	}
}

// With nothing readable recording a turn, an unreadable entry alone refuses a normal verb and not a recovery one.
func TestAnUnreadableRegistryEntryAloneRefusesOnlyTheNonRecoveryGate(t *testing.T) {
	registerBoth(t, false)
	if why := opgate.RecoveryRefusal(); why != "" {
		t.Fatalf("recovery was locked out by an unreadable entry: %q", why)
	}
	if why := opgate.Refusal(); !strings.Contains(why, "cannot tell") {
		t.Fatalf("the non-recovery gate let an unreadable live-host entry through: %q", why)
	}
}

// The gate only reads the database the process names: a database at an older schema (a migration is
// pending, coder_sessions absent) keeps its bytes, its schema and its directory.
func TestTheGateNeverCreatesOrMigratesTheNamedDatabase(t *testing.T) {
	coderhost.SetRegistryDirForTest(t, "")
	cfg := opEnv(t)
	dir := t.TempDir()
	cfg.DBPath = filepath.Join(dir, "old.db")
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`, `INSERT INTO schema_migrations VALUES (1)`, `CREATE TABLE settings (k TEXT, v TEXT)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	snapshot := func() (sum [32]byte, files []string) {
		b, err := os.ReadFile(cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			files = append(files, e.Name())
		}
		return sha256.Sum256(b), files
	}
	sumBefore, filesBefore := snapshot()
	schema := func() string {
		ro, err := sql.Open("sqlite", readOnlyURI(cfg.DBPath))
		if err != nil {
			t.Fatal(err)
		}
		defer ro.Close()
		var out []string
		rows, err := ro.Query(`SELECT name FROM sqlite_master ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, n)
		}
		return strings.Join(out, ",")
	}
	schemaBefore := schema()

	if why := opgate.Refusal(); why != "" {
		t.Fatalf("the operator was refused by an old database: %q", why)
	}
	if why := opgate.RecoveryRefusal(); why != "" {
		t.Fatalf("the operator was refused (recovery) by an old database: %q", why)
	}
	sumAfter, filesAfter := snapshot()
	if sumAfter != sumBefore {
		t.Error("the gate changed the bytes of the database it was pointed at (it migrated or wrote it)")
	}
	if strings.Join(filesAfter, ",") != strings.Join(filesBefore, ",") {
		t.Errorf("the gate left files next to the database: %v -> %v", filesBefore, filesAfter)
	}
	if got := schema(); got != schemaBefore {
		t.Errorf("the schema changed: %q -> %q", schemaBefore, got)
	}
}

// The named database marks the caller as a turn while the registry holds an unreadable entry: the recovery
// flavour skips the entry, never the named database, so it still refuses; the other flavour refuses too.
func TestARecoveryVerbIsRefusedForATurnInTheNamedDatabaseWhateverTheRegistryHolds(t *testing.T) {
	registerBoth(t, false) // the unreadable entry only
	named := filepath.Join(t.TempDir(), "named.db")
	seedMigratedDB(t, named)
	markThisProcessAsACoderTurn(t, &globalConfig{DBPath: named})
	opgate.SetForTest(t, (&globalConfig{DBPath: named}).gateRefusal)
	if why := opgate.RecoveryRefusal(); !strings.Contains(why, "coder session's turn") {
		t.Fatalf("recovery skipped the named database after a registry error: %q", why)
	}
	if why := opgate.Refusal(); why == "" {
		t.Fatal("the non-recovery gate let a turn through")
	}
}

// A database at the schema before the turn-bound migration (152 added turn_bound; the columns the gate
// reads came with the table in 129) is read fine: no lockout on upgrade, and a marked turn still refuses.
func TestADatabaseBeforeTheTurnBoundMigrationIsReadByTheGate(t *testing.T) {
	coderhost.SetRegistryDirForTest(t, "")
	cfg := opEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "151.db")
	seedMigratedDB(t, cfg.DBPath)
	d, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec("ALTER TABLE coder_sessions DR" + "OP COLUMN turn_bound"); err != nil {
		t.Skip("cannot take the column away here: ", err)
	}
	d.Close()
	if why := opgate.Refusal(); why != "" {
		t.Fatalf("the operator was locked out by a database before migration 152: %q", why)
	}
	if coderhost.ProcStamp(os.Getpid()) == "" {
		return
	}
	d, _ = storage.NewDatabase(cfg.DBPath)
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, state, role, turn_id, turn_pid, turn_stamp, created_at, updated_at) VALUES ('default', 'old', 'running', 'solo', 't1', ?, ?, 'n', 'n')`, os.Getpid(), coderhost.ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	if why := opgate.Refusal(); !strings.Contains(why, "coder session's turn") {
		t.Fatalf("a turn recorded before migration 152 was not seen: %q", why)
	}
}
