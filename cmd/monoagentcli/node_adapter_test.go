package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/zalando/go-keyring"
)

// TestNodeAdapterWiring pins what nodeDeps hands internal/cli/node: --profile by name resolved
// through the real database before the node runs, --json read live, the CLI's exit codes 2 and
// 3, the real registry, and `node palette` added to the group.
func TestNodeAdapterWiring(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dbPath := filepath.Join(home, "node-adapter.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('work', 'Work', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, args ...string) (string, string, int) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(stdin)
		w.Close()
		orig := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = orig }()
		root := newRootCmd()
		var errOut bytes.Buffer
		root.SetErr(&errOut)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(append([]string{"--db-path", dbPath}, args...))
		var runErr error
		out := captureStdout(t, func() { runErr = root.Execute() })
		if runErr != nil {
			errOut.WriteString(runErr.Error())
		}
		return out, errOut.String(), exitCodeFor(runErr)
	}

	// --profile Work is resolved to "work" before the vault node runs in it.
	payload := `{"config":{"name":"adapter-secret","field_keys":["token"]},"input":[{"json":{"token":"t"}}]}`
	if _, errOut, code := run(payload, "--profile", "Work", "node", "run", "vault.secret_save", "--stdin", "--output", "json"); code != 0 {
		t.Fatalf("node run: code %d, stderr %q", code, errOut)
	}
	var profile string
	if err := db.DB.QueryRow(`SELECT profile_id FROM vault_secrets WHERE name = 'adapter-secret'`).Scan(&profile); err != nil || profile != "work" {
		t.Fatalf("secret saved in profile %q (%v), want work", profile, err)
	}

	if _, errOut, code := run("", "node", "run", "no.such_node"); code != 2 || !strings.Contains(errOut, "unknown node type") {
		t.Errorf("unknown node: code %d, stderr %q", code, errOut)
	}
	if _, errOut, code := run("", "node", "schema", "no.such_node"); code != 2 {
		t.Errorf("unknown schema: code %d, stderr %q", code, errOut)
	}
	// curl takes no flags (it parses its own), so it runs without --db-path.
	curl := newNodeCmd(&globalConfig{})
	curl.SetArgs([]string{"curl", "-"})
	curl.SetIn(strings.NewReader(""))
	if err := curl.Execute(); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "no curl command on stdin") {
		t.Errorf("empty curl: %v", err)
	}
	out, _, code := run("", "--json", "node", "list", "--filter", "trigger.")
	var entries []map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &entries) != nil || len(entries) == 0 {
		t.Fatalf("node list --json: code %d, out %q", code, out)
	}
	if out, _, code := run("", "node", "list", "--filter", "core.if"); code != 0 || !strings.Contains(out, "core.if") {
		t.Errorf("node list: code %d, out %q", code, out)
	}
	if _, _, code := run("", "node", "palette", "--help"); code != 0 {
		t.Errorf("node palette is not in the group: code %d", code)
	}
}
