package main

import (
	"bytes"
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"strings"
	"testing"
)

// runGateCLIOn is runGateCLI over a given database, so one test can run
// several invocations against the same file.
func runGateCLIOn(dbPath string, args ...string) (string, error) {
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"--db-path", dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return errb.String() + out.String(), err
}

// --all-profiles is a deliberate cross-profile read: with it the gate needs no
// --profile; without either the command is refused; with both it still runs
// where it did before.
func TestAllProfilesFlagMakesTheCommandGlobal(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	cases := []struct {
		name   string
		args   []string // the command without --all-profiles
		bothOK bool     // the command also accepts --profile next to --all-profiles
	}{
		{"summary", []string{"--json", "summary"}, true},
		{"org summary", []string{"--json", "org", "summary", "--fast"}, true},
		{"workflow list", []string{"--json", "workflow", "list"}, true},
		{"workflow executions", []string{"--json", "workflow", "executions", "--all"}, true},
		{"api key list", []string{"--json", "api", "key", "list"}, true},
		{"capture list", []string{"--json", "capture", "list"}, false},
		{"profile documents index", []string{"--json", "profile", "documents", "index", "--all"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "gate.db")
			// Create the database: capture list reads it read-only.
			if _, err := runGateCLIOn(dbPath, "--profile", "default", "people", "list"); err != nil {
				t.Fatalf("seeding the database: %v", err)
			}
			withFlag := append(append([]string{}, tc.args...), "--all-profiles")
			out, err := runGateCLIOn(dbPath, withFlag...)
			if err != nil || strings.Contains(out, "acts on a profile") {
				t.Fatalf("--all-profiles alone: got %v / %q", err, out)
			}
			if _, err := runGateCLIOn(dbPath, tc.args...); err == nil || !strings.Contains(err.Error(), "acts on a profile") {
				t.Fatalf("without --all-profiles or --profile: got %v, want the gate's refusal", err)
			}
			both := append([]string{"--profile", "default"}, withFlag...)
			out, err = runGateCLIOn(dbPath, both...)
			if tc.bothOK && err != nil {
				t.Fatalf("both flags: got %v / %q", err, out)
			}
			if !tc.bothOK && err == nil {
				t.Fatalf("both flags: want the command's own rejection, got %q", out)
			}
		})
	}
}
