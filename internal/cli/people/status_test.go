package people

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// newStatusCLITestDB applies every real migration (via the same
// storage.NewDatabase/ApplyMigrations path initDB uses) to a fresh SQLite
// file, then seeds one person, "p1", under profile "default". Each command
// under test opens its own connection through initDB afterward; since
// schema_migrations is already populated, ApplyMigrations is a no-op there.
func newStatusCLITestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cli-status-test.db")

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1','alice','x','default')`); err != nil {
		t.Fatalf("seeding p1: %v", err)
	}
	// initDB resolves --profile against a real row in `profiles` (by id or
	// name) before running any command logic, so a "work" profile must exist
	// for tests to reach person/profile-scoping behavior rather than failing
	// earlier on profile resolution.
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('work', 'Work')`); err != nil {
		t.Fatalf("seeding work profile: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

func TestPeopleStatusSetAndGet(t *testing.T) {
	dbPath := newStatusCLITestDB(t)
	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}

	setCmd := testDeps(cfg).newPeopleStatusSetCmd()
	setCmd.SetArgs([]string{"p1", "Just closed the Q1 deal"})
	setOut := captureStdout(t, func() {
		if err := setCmd.Execute(); err != nil {
			t.Fatalf("set: %v", err)
		}
	})
	if !bytes.Contains([]byte(setOut), []byte("Posted status update")) {
		t.Errorf("set output = %q, want it to mention 'Posted status update'", setOut)
	}

	getCmd := testDeps(cfg).newPeopleStatusGetCmd()
	getCmd.SetArgs([]string{"p1"})
	getOut := captureStdout(t, func() {
		if err := getCmd.Execute(); err != nil {
			t.Fatalf("get: %v", err)
		}
	})
	if !bytes.Contains([]byte(getOut), []byte("Just closed the Q1 deal")) {
		t.Errorf("get output = %q, want it to contain the posted text", getOut)
	}
}

func TestPeopleStatusGetNoneSetYet(t *testing.T) {
	dbPath := newStatusCLITestDB(t)
	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}

	getCmd := testDeps(cfg).newPeopleStatusGetCmd()
	getCmd.SetArgs([]string{"p1"})
	out := captureStdout(t, func() {
		if err := getCmd.Execute(); err != nil {
			t.Fatalf("get: %v", err)
		}
	})
	if !bytes.Contains([]byte(out), []byte("No status set for this person.")) {
		t.Errorf("get output = %q, want the no-status message", out)
	}
}

func TestPeopleStatusHistoryNewestFirst(t *testing.T) {
	dbPath := newStatusCLITestDB(t)
	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}

	for _, text := range []string{"first update", "second update"} {
		setCmd := testDeps(cfg).newPeopleStatusSetCmd()
		setCmd.SetArgs([]string{"p1", text})
		if err := setCmd.Execute(); err != nil {
			t.Fatalf("set(%q): %v", text, err)
		}
	}

	historyCmd := testDeps(cfg).newPeopleStatusHistoryCmd()
	historyCmd.SetArgs([]string{"p1"})
	out := captureStdout(t, func() {
		if err := historyCmd.Execute(); err != nil {
			t.Fatalf("history: %v", err)
		}
	})

	firstIdx := bytes.Index([]byte(out), []byte("first update"))
	secondIdx := bytes.Index([]byte(out), []byte("second update"))
	if firstIdx == -1 || secondIdx == -1 {
		t.Fatalf("history output missing an entry: %q", out)
	}
	if secondIdx > firstIdx {
		t.Errorf("history output not newest-first: %q", out)
	}
}

// TestPeopleStatusWrongProfile is the regression case the design spec called
// for: a person belonging to one profile must be invisible to and
// unwritable from another profile via the CLI, matching the repository
// layer's guarantee (internal/storage/person_status_test.go covers the
// repository directly; this covers the CLI's profile-scoping wiring on top
// of it, e.g. that `set` actually threads cfg.ProfileID through rather than
// silently defaulting or leaking cross-profile).
func TestPeopleStatusWrongProfile(t *testing.T) {
	dbPath := newStatusCLITestDB(t)

	// p1 belongs to "default". Try to post a status for it while the active
	// profile is "work" — must fail, not silently attach to the wrong
	// profile's person.
	wrongProfileCfg := &testConfig{DBPath: dbPath, ProfileID: "work"}
	setCmd := testDeps(wrongProfileCfg).newPeopleStatusSetCmd()
	setCmd.SetArgs([]string{"p1", "should not be allowed"})
	if err := setCmd.Execute(); err == nil {
		t.Fatal("set: expected an error when person does not belong to the active profile, got nil")
	}

	// Now post a real status for p1 under its correct profile ("default").
	correctCfg := &testConfig{DBPath: dbPath, ProfileID: "default"}
	okSetCmd := testDeps(correctCfg).newPeopleStatusSetCmd()
	okSetCmd.SetArgs([]string{"p1", "visible only in default"})
	if err := okSetCmd.Execute(); err != nil {
		t.Fatalf("set under correct profile: %v", err)
	}

	// Reading it back "as work" (the wrong profile for p1) must show nothing,
	// not the status just posted under "default".
	getCmd := testDeps(wrongProfileCfg).newPeopleStatusGetCmd()
	getCmd.SetArgs([]string{"p1"})
	out := captureStdout(t, func() {
		if err := getCmd.Execute(); err != nil {
			t.Fatalf("get (wrong profile): %v", err)
		}
	})
	if bytes.Contains([]byte(out), []byte("visible only in default")) {
		t.Fatalf("get (wrong profile) leaked another profile's status: %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("No status set for this person.")) {
		t.Errorf("get (wrong profile) output = %q, want the no-status message", out)
	}
}
