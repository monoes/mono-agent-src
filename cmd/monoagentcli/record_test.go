package main

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/recording"
)

// TestRecordAdapterWiring pins what recordDeps hands internal/cli/record: --profile by name
// through the profile database, the live --json and --profile values, the CLI's exit codes, and
// the real --json error document (one document, no second one from main).
func TestRecordAdapterWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(capture.InboxEnv, filepath.Join(home, "inbox"))
	t.Cleanup(func() { _ = recording.SetProfile("") })

	dbPath := filepath.Join(home, "monoagent.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT, created_at TEXT, root_dir TEXT)`,
		`INSERT INTO profiles (id, name, created_at) VALUES ('p-work', 'Work', '2026-01-01')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	cfg := &globalConfig{DBPath: dbPath}
	d := recordDeps(cfg)
	if got := d.ResolveProfile("Work"); got != "p-work" {
		t.Errorf("ResolveProfile(Work) = %q, want p-work", got)
	}
	if got := d.ResolveProfile("nope"); got != "nope" {
		t.Errorf("ResolveProfile(nope) = %q, want it unchanged", got)
	}
	if got := recordDeps(&globalConfig{DBPath: filepath.Join(home, "missing.db")}).ResolveProfile("Work"); got != "Work" {
		t.Errorf("ResolveProfile without a database = %q, want it unchanged", got)
	}

	cfg.JSONOutput, cfg.ProfileID = true, "Work"
	if !d.JSONOutput() || d.ProfileID() != "Work" {
		t.Errorf("Deps read the flags when built, not when a command runs")
	}
	if code := exitCodeFor(d.InvalidInput("x")); code != 3 {
		t.Errorf("InvalidInput exit code = %d, want 3", code)
	}
	var rep reportedError
	if !errors.As(d.Reported(errors.New("x")), &rep) {
		t.Errorf("Reported is not the CLI's reportedError")
	}

	cfg.ProfileID = ""
	cmd := newRecordCmd(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"show", "nope"})
	err = cmd.Execute()
	if !errors.As(err, &rep) {
		t.Errorf("err = %v, want a reportedError", err)
	}
	if want := "{\"error\":\"recording not found: nope\"}\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}
