// cmd/monoagentcli/application_test.go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func newApplicationCLITestDB(t *testing.T) string {
	t.Helper()
	// "application apply"/"profile upload-document" write documents via
	// vault.RegisterDocument, which resolves its path via profiledir.Root —
	// falling back to the real $HOME when the test DB has no
	// profiles.root_dir override. Without this, every run pollutes the
	// developer's actual ~/.monoagent vault.
	t.Setenv("HOME", t.TempDir())
	// The vault syncs each file into the knowledge graph in the background;
	// let it finish before HOME is removed. (MONOMIND_BIN is left alone:
	// tests sharing this helper put their own fake monomind on PATH.)
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "cli-application-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

// `application` lives in internal/cli/application (tested there); this pins its adapter: the
// active profile, --json, the CLI's table and exit codes reach the commands.
func TestApplicationAdapterWiring(t *testing.T) {
	dbPath := newApplicationCLITestDB(t)
	seedProfile(t, dbPath, "work")
	run := func(profile string, jsonOut bool, args ...string) (string, error) {
		cmd := newApplicationCmd(&globalConfig{DBPath: dbPath, ProfileID: profile, JSONOutput: jsonOut})
		cmd.SetArgs(args)
		var out, errb bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("work", true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "https://acme.example/1")
	if err != nil || !strings.HasPrefix(out, "{\n  \"id\": ") {
		t.Fatalf("add --json: %q, %v", out, err)
	}
	if out, err := run("work", false, "list"); err != nil || !strings.Contains(out, "Backend Engineer") || !strings.Contains(out, "STATUS") {
		t.Errorf("list as work, in the CLI's table: %q, %v", out, err)
	}
	if out, err := run("default", false, "list"); err != nil || out != "No applications found.\n" {
		t.Errorf("the default profile sees work's application: %q, %v", out, err)
	}
	if _, err := run("work", true, "get", "does-not-exist"); exitCode(err) != 2 {
		t.Errorf("get of an unknown application: exit %d (%v), want 2", exitCode(err), err)
	}
	if _, err := run("work", true, "add", "--kind", "gig", "--title", "x", "--url", "u"); exitCode(err) != 3 {
		t.Errorf("add of an unknown kind: exit %d (%v), want 3", exitCode(err), err)
	}
}
