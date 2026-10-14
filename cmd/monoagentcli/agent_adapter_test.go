package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// TestAgentAdapterWiring pins what agentDeps hands internal/cli/agent: the database, --json
// read live, the CLI's exit code 3 and its table.
func TestAgentAdapterWiring(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent-adapter.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	run := func(args ...string) (string, string, int) {
		t.Helper()
		root := newRootCmd()
		var errOut bytes.Buffer
		root.SetErr(&errOut)
		root.SetArgs(append([]string{"--db-path", dbPath}, args...))
		var runErr error
		out := captureStdout(t, func() { runErr = root.Execute() })
		if runErr != nil {
			errOut.WriteString(runErr.Error())
		}
		return out, errOut.String(), exitCodeFor(runErr)
	}

	out, errOut, code := run("--json", "agent", "roster", "add", "codex", "gpt-x")
	var added map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &added) != nil || added["added"] != true {
		t.Fatalf("roster add: code %d, out %q, stderr %q", code, out, errOut)
	}
	if out, _, code := run("agent", "roster", "--no-scan"); code != 0 || !strings.Contains(out, "RUNTIME") || !strings.Contains(out, "gpt-x") {
		t.Fatalf("roster: code %d, out %q", code, out)
	}
	for _, args := range [][]string{
		{"agent", "roster", "auto-revalidate", "on", "--per-day", "999"},
		{"agent", "validate", "--all", "--runtime", "codex"},
		{"agent", "validate", "--model", "m"},
	} {
		if _, errOut, code := run(args...); code != 3 {
			t.Errorf("%v: code %d (stderr %q), want 3", args, code, errOut)
		}
	}
	if out, _, code := run("--json", "agent", "roster", "remove", "codex", "gpt-x"); code != 0 || !strings.Contains(out, `"removed": true`) {
		t.Fatalf("roster remove: code %d, out %q", code, out)
	}
}

// `agent install <id> --json` for a runtime that is already there ends
// with a done event that says so, not a bare {"kind":"done"}.
func TestFinishStreamedNoOpSaysAlreadyInstalled(t *testing.T) {
	var out strings.Builder
	msg := "claude is already installed (2.0.1 at /usr/bin/claude) — nothing to do; use --force to reinstall"
	if err := finishStreamed(&out, true, nil, msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"kind":"done"`) || !strings.Contains(out.String(), "already installed") {
		t.Fatalf("done event %s", out.String())
	}
}
