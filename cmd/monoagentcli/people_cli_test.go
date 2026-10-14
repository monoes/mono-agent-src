package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/peoplereview"
	"github.com/monoes/mono-agent/internal/storage"
)

// The people commands' tests live in internal/cli/people. These helpers stay here for the
// tests in main that share them (login names, agent validate and many more).

// newReviewCLITestDB seeds a migrated DB with two people awaiting review
// and returns a config pointing at it. HOME is isolated so the workflow
// file store is empty.
func newReviewCLITestDB(t *testing.T) (*globalConfig, *storage.Database) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "review.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p1", "p2"} {
		if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, full_name, category, introduction)
			VALUES (?, 'default', 'LINKEDIN', ?, 'Sam', ?, 'drafted')`, id, id+"-user", peoplereview.Pending); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}, db
}

func exitCode(err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	if err != nil {
		return 1
	}
	return 0
}

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

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it — many commands print directly via
// fmt.Fprintf(os.Stdout, ...), not through cobra's OutOrStdout(), so this
// is the only way to observe it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

// People saved without counts or a verified flag (NULL columns, e.g. from a
// sparse import) must still export; listing and showing them is tested in
// internal/cli/people.
func TestPeopleNullColumnsExport(t *testing.T) {
	_, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`UPDATE people SET content_count = NULL, following_count = NULL, is_verified = NULL`); err != nil {
		t.Skipf("schema keeps these columns NOT NULL: %v", err)
	}
	if n, err := exportPeopleData(db, t.TempDir(), "default"); err != nil || n != 2 {
		t.Fatalf("export = %d, %v", n, err)
	}
}

// TestPeopleAdapterWiring pins what the adapter hands internal/cli/people: initDB (the active
// profile, --profile by name), --json through stdout, the CLI's exit codes, the workflow store
// and `workflow run` for review sends, and the node registry and credentials for Outlook.
func TestPeopleAdapterWiring(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username) VALUES ('w1', 'work', 'X', 'w1-user')`); err != nil {
		t.Fatal(err)
	}
	run := func(cfg *globalConfig, args ...string) (string, error) {
		var runErr error
		out := captureStdout(t, func() {
			cmd := newPeopleCmd(cfg)
			cmd.SetArgs(args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			runErr = cmd.Execute()
		})
		return out, runErr
	}

	if out, err := run(cfg, "count"); err != nil || out != "{\n  \"count\": 2\n}\n" {
		t.Fatalf("count = %q, %v", out, err)
	}
	if out, err := run(&globalConfig{DBPath: cfg.DBPath, JSONOutput: true, ProfileID: "Work"}, "count"); err != nil || !strings.Contains(out, `"count": 1`) {
		t.Fatalf("count --profile Work = %q, %v", out, err)
	}
	if _, err := run(&globalConfig{DBPath: cfg.DBPath, ProfileID: "nope"}, "count"); exitCode(err) != 3 {
		t.Fatalf("count --profile nope: %v, want exit 3", err)
	}
	if _, err := run(cfg, "get", "nope"); exitCode(err) != 2 {
		t.Fatalf("get nope: %v, want exit 2", err)
	}
	if _, err := run(cfg, "list", "--limit", "-1"); exitCode(err) != 3 {
		t.Fatalf("list --limit -1: %v, want exit 3", err)
	}

	// Review sends find their workflow in the CLI's store and run it with `workflow run`.
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id, is_active) VALUES ('wf-off', 'Off', 'default', 0), ('wf-on', 'On', 'default', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(cfg, "review", "approve", "p1", "--send", "--send-workflow", "Off"); exitCode(err) != 3 || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("approve --send to an inactive workflow: %v, want exit 3", err)
	}
	if _, err := run(cfg, "review", "approve", "p1", "--send", "--send-workflow", "On"); err == nil || !strings.Contains(err.Error(), "p1 is approved, but sending failed: execution") {
		t.Fatalf("approve --send to an empty workflow: %v, want the engine run's failure", err)
	}

	// Outlook goes through the CLI's registry and connection store: an unknown connection fails
	// there, before anything is sent or recorded.
	if _, err := run(cfg, "messages", "compose", "p2", "--connection", "nope", "--to", "a@example.com", "--body", "hi"); err == nil || !strings.Contains(err.Error(), `resolving connection "nope"`) {
		t.Fatalf("compose via an unknown connection: %v", err)
	}
}
