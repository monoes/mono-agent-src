package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/zalando/go-keyring"
)

// newLoginTestDB is a migrated database in a temp dir, with the keyring mocked.
func newLoginTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/login-test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

// sessionsTestEnv is a temp HOME with a migrated DB holding a "work"
// profile beside "default".
type sessionsTestEnv struct {
	home   string
	dbPath string
	db     *storage.Database
}

func newSessionsTestEnv(t *testing.T) *sessionsTestEnv {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dbPath := filepath.Join(home, ".monoagent", "monoagent.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name, created_at) VALUES ('default', 'Default', '2026-01-01'), ('work', 'Work', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	return &sessionsTestEnv{home: home, dbPath: dbPath, db: db}
}

// run executes `monoagentcli <args>` with stdin and returns stdout, stderr
// and the exit code main would use.
func (e *sessionsTestEnv) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append(args, "--db-path", e.dbPath))
	err := root.Execute()
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), exitCodeFor(err)
}

// TestLoginAdapterWiring pins what loginDeps hands internal/cli/login: --profile by name
// resolved through the real database before the profile is read, --json read live, the
// CLI's exit codes 2, 3 and 4, its JSON writer and its table, and logout's "default".
func TestLoginAdapterWiring(t *testing.T) {
	e := newSessionsTestEnv(t)
	ctx := context.Background()
	if err := upsertSessionRow(ctx, e.db.DB, "work", "linkedin", "me", []byte(`[{"name":"sid","value":"v"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := upsertSessionRow(ctx, e.db.DB, "work", "x", "me", []byte(`[{"name":"sid","value":"v"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := upsertSessionRow(ctx, e.db.DB, "default", "tiktok", "me", []byte(`[{"name":"sid","value":"v"}]`)); err != nil {
		t.Fatal(err)
	}
	id := func(platform string) string {
		var n int
		if err := e.db.DB.QueryRow(`SELECT id FROM crawler_sessions WHERE platform = ?`, platform).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return strconv.Itoa(n)
	}
	if _, err := e.db.DB.Exec(`UPDATE crawler_sessions SET expiry = ? WHERE platform = 'x'`, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	// --profile by name: Work is resolved to "work" before the session is looked up.
	out, errOut, code := e.run(t, "", "--profile", "Work", "--json", "login", "test", id("linkedin"))
	var doc map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil || doc["status"] != "ok" || !strings.HasSuffix(out, "}\n") {
		t.Fatalf("login test: code %d, out %q, stderr %q", code, out, errOut)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--profile", "Work", "login", "test", id("x")}, 4},           // expired
		{[]string{"--profile", "Work", "login", "test", id("tiktok")}, 2},      // another profile's
		{[]string{"--profile", "Work", "login", "test", "abc"}, 3},             // not an id
		{[]string{"--profile", "Work", "login", "delete", "99999"}, 2},         // unknown
		{[]string{"--profile", "Work", "--json", "login", "delete", "abc"}, 3}, // not an id
	} {
		if _, errOut, code := e.run(t, "", tc.args...); code != tc.code {
			t.Errorf("%v: code %d (stderr %q), want %d", tc.args, code, errOut, tc.code)
		}
	}

	// The table is the CLI's, its header upper-cased.
	out, _, code = e.run(t, "", "--profile", "work", "login", "status")
	if code != 0 || !strings.Contains(out, "PLATFORM") || !strings.Contains(out, "linkedin") {
		t.Fatalf("login status: code %d, out %q", code, out)
	}
	// logout without --profile acts on "default".
	if _, errOut, code := e.run(t, "", "logout", "tiktok"); code != 0 {
		t.Fatalf("logout: code %d, stderr %q", code, errOut)
	}
	var left int
	if err := e.db.DB.QueryRow(`SELECT COUNT(*) FROM crawler_sessions WHERE platform = 'tiktok'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("logout left %d tiktok sessions (%v)", left, err)
	}
	// The registry stays out of a test binary: an unknown target names only the bots.
	if _, errOut, code := e.run(t, "", "login", "no-such-automation"); code == 0 || !strings.Contains(errOut, "unsupported platform") {
		t.Fatalf("login unknown: code %d, stderr %q", code, errOut)
	}
}
