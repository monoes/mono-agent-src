package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"

	_ "modernc.org/sqlite"
)

func TestResolveProfileIDMatchesNameCaseInsensitively(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT, created_at TEXT)`,
		`INSERT INTO profiles (id, name) VALUES ('p-work', 'Work')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []string{"p-work", "Work", "work", "WORK"} {
		if got, err := resolveProfileID(db, in); err != nil || got != "p-work" {
			t.Errorf("resolveProfileID(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := resolveProfileID(db, "nope"); err == nil {
		t.Error("unknown profile resolved")
	}
	if _, err := resolveProfileID(db, "_machine"); err == nil {
		t.Error("the reserved machine scope resolved as a profile id")
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES ('p-odd', '_machine')`); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveProfileID(db, "_machine"); err != nil || got != "p-odd" {
		t.Errorf("a profile named _machine resolved to %q, %v", got, err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES ('p-work2', 'work')`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProfileID(db, "WORK"); err == nil || exitCodeFor(err) != 3 {
		t.Errorf("a name two profiles share must be an invalid-input error, got %v", err)
	}
	if got, err := resolveProfileID(db, "p-work"); err != nil || got != "p-work" {
		t.Errorf("an exact id still resolves: %q, %v", got, err)
	}
}

func TestResolveProfileIDWithNoProfilesPointsToCreate(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	_, err = resolveProfileID(db, "foo")
	if err == nil || !strings.Contains(err.Error(), "monoagentcli profile create <name>") {
		t.Fatalf("got %v", err)
	}
}

// With no home directory the database path "~/.monoagent/..." must fail, not become
// a literal ./~/.monoagent next to the caller.
func TestInitDBWithoutAHomeDoesNotCreateALiteralTilde(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	testhome.SetHome(t, "") // empty: the home cannot be resolved
	_, err := initDB(&globalConfig{DBPath: "~/.monoagent/monoagent.db"})
	if err == nil || !strings.Contains(err.Error(), "set HOME") {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "~")); statErr == nil {
		t.Fatal("a literal ./~ directory was created")
	}
}
