package dbcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// deps points the command at the database at path, opened (and migrated) as the CLI's initDB does.
func deps(path string, jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(path)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		DBPath: func() string { return path },
	}
}

func run(t *testing.T, d Deps) (string, string) {
	t.Helper()
	cmd := NewCommand(d)
	cmd.SetArgs([]string{"migrate"})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("db migrate: %v\n%s", err, errOut.String())
	}
	return out.String(), errOut.String()
}

// A fresh database is migrated and says how far; the second run applies nothing.
func TestMigrateFromNothingThenNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monoagent.db")
	var first Result
	out, _ := run(t, deps(path, true))
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatalf("not a result: %v\n%s", err, out)
	}
	if first.Applied == 0 || first.SchemaVersion == 0 || first.Profiles == nil || first.Errors == nil {
		t.Fatalf("first run: %+v", first)
	}
	// The JSON the desktop app reads: every field, arrays never null.
	for _, key := range []string{`"schema_version"`, `"applied"`, `"profiles": [`, `"vault_moved"`, `"errors": [`} {
		if !strings.Contains(out, key) {
			t.Errorf("--json lacks %s:\n%s", key, out)
		}
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("database not created: %v", err)
	}

	text, _ := run(t, deps(path, false))
	if !strings.HasPrefix(text, "schema version ") || !strings.Contains(text, "(0 migration(s) applied)\n") || !strings.Contains(text, " profile(s), 0 vault file(s) moved\n") {
		t.Errorf("second run, text:\n%s", text)
	}
}
