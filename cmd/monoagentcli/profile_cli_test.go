package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
)

// The profile commands' tests live in internal/cli/profile. These helpers stay here for the
// tests in main that share them (image sync, org reconcile).

func newProfileDocsCLITestDB(t *testing.T) string {
	t.Helper()
	return newApplicationCLITestDB(t) // reuses the shared migration-seeding helper from application_test.go
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v carries no exit code", err)
	}
	return ce.code
}

func runFolderSync(t *testing.T, cfg *globalConfig, newCmd func(*globalConfig) *cobra.Command, args ...string) folderSyncResult {
	t.Helper()
	cmd := newCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v (%s)", args, err, out.String())
	}
	var res folderSyncResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	return res
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedProfiles(t *testing.T, dbPath string, ids ...string) {
	t.Helper()
	store, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range ids {
		if _, err := store.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
}

func syncCounts(r folderSyncResult) [3]int { return [3]int{r.Added, r.Updated, r.Removed} }

// TestProfileAdapterWiring pins what the adapter hands internal/cli/profile: initDB (the
// active profile, --profile), --json through printJSON on stdout, and the CLI's exit codes.
func TestProfileAdapterWiring(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	seedProfiles(t, dbPath, "work")

	run := func(cfg *globalConfig, args ...string) (string, error) {
		var runErr error
		out := captureStdout(t, func() {
			cmd := newProfileCmd(cfg)
			cmd.SetArgs(args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			runErr = cmd.Execute()
		})
		return out, runErr
	}

	out, err := run(&globalConfig{DBPath: dbPath, JSONOutput: true}, "current")
	if err != nil || !bytes.Contains([]byte(out), []byte(`"id": "default"`)) {
		t.Fatalf("current = %q, %v", out, err)
	}
	if _, err := run(&globalConfig{DBPath: dbPath, JSONOutput: true}, "switch", "work"); err != nil {
		t.Fatal(err)
	}
	out, err = run(&globalConfig{DBPath: dbPath, JSONOutput: true}, "get", "work")
	if err != nil || !bytes.Contains([]byte(out), []byte(`"active": true`)) {
		t.Fatalf("get work = %q, %v", out, err)
	}
	if _, err := run(&globalConfig{DBPath: dbPath}, "get", "nope"); exitCodeOf(t, err) != 2 {
		t.Fatalf("get nope: %v, want exit 2", err)
	}
	if _, err := run(&globalConfig{DBPath: dbPath}, "create", " "); exitCodeOf(t, err) != 3 {
		t.Fatalf("create blank: %v, want exit 3", err)
	}
	// --profile reaches the command through initDB.
	var root bytes.Buffer
	cmd := newProfileCmd(&globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "work"})
	cmd.SetArgs([]string{"documents", "sync"})
	cmd.SetOut(&root)
	if err := cmd.Execute(); err != nil || !bytes.Contains(root.Bytes(), []byte(`"profile_id": "work"`)) {
		t.Fatalf("documents sync --profile work = %q, %v", root.String(), err)
	}
}
