package main

import (
	"bytes"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/profilegate"
	"github.com/monoes/mono-agent/internal/storage"
)

// The gate enforces by default: it refuses a scoped command run without --profile
// before any of its work, with the CLI's invalid-input exit code.
func TestProfileGateEnforceEndToEnd(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "gate.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('p-work', 'work', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if profileGateMode != profilegate.ModeEnforce {
		t.Fatal("the profile gate must enforce by default")
	}

	run := func(args ...string) (string, string, error) {
		root := newRootCmd()
		var errOut bytes.Buffer
		root.SetErr(&errOut)
		root.SetArgs(append([]string{"--db-path", dbPath}, args...))
		var runErr error
		out := captureStdout(t, func() { runErr = root.Execute() })
		return out, errOut.String(), runErr
	}

	out, _, err := run("people", "list")
	if err == nil || exitCodeFor(err) != 3 {
		t.Fatalf("people list without --profile: err %v (code %d), want exit 3", err, exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "--profile") || !strings.Contains(err.Error(), "work (p-work)") {
		t.Errorf("error does not name the profiles: %v", err)
	}
	if out != "" {
		t.Errorf("the refused command printed %q", out)
	}
	// Global commands and the profile list need no profile.
	for _, args := range [][]string{{"config", "list"}, {"version"}, {"node", "list"}, {"message-template", "list"}, {"profile", "list"}, {"profile", "get", "work"}} {
		if _, _, err := run(args...); err != nil {
			t.Errorf("%v (global) without --profile: %v", args, err)
		}
	}
	// agent scan is global too, but needs monomind (absent on CI): it may fail
	// for that, never at the gate.
	if _, _, err := run("agent", "scan"); err != nil && (exitCodeFor(err) == 3 || strings.Contains(err.Error(), "pass --profile")) {
		t.Errorf("agent scan was gated: %v", err)
	}
	// There is no active profile: switch and current are gone, and profile list
	// carries no "active" field.
	for _, sub := range []string{"switch", "current"} {
		if _, _, err := run("profile", sub, "work"); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("profile %s: %v, want an unknown-command error", sub, err)
		}
	}
	if out, _, err := run("--json", "profile", "list"); err != nil || strings.Contains(out, `"active"`) {
		t.Errorf("profile list --json: %v, %q", err, out)
	}
	// doctor runs the machine checks without a profile; the per-profile ones
	// ask for --profile.
	out, _, err = run("--json", "doctor", "--group", "core")
	if err != nil && exitCodeFor(err) == 3 {
		t.Errorf("doctor without --profile was refused: %v", err)
	}
	if !strings.Contains(out, "core.home") || !strings.Contains(out, "pass --profile") {
		t.Errorf("doctor without --profile: want the machine checks and a skipped profile check, got %q", out)
	}
	if _, _, err := run("people"); err != nil {
		t.Errorf("bare people (help): %v", err)
	}
	if _, _, err := run("people", "list", "--profile", "work"); err != nil {
		t.Errorf("people list --profile work: %v", err)
	}
}
