//go:build !windows

package org

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testhome"
)

// noRunMonomind fails `org logs` the way monomind 2.29 does for an org that
// never ran; org "broken" fails for another reason.
func noRunMonomind(t *testing.T) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.29.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi
if [ "$3" = "broken" ]; then
  echo '[ERROR] bus.jsonl unreadable' >&2
  exit 1
fi
echo "[ERROR] no runs found for org $3 — start one with: monomind org run $3" >&2
exit 1
`
	bin := filepath.Join(t.TempDir(), "norun-monomind.sh") // outside the project: a project-local one is refused
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
}

func runOrgLogs(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	cmd := newOrgCmd(&testConfig{DBPath: filepath.Join(t.TempDir(), "o.db"), ProfileID: "default"})
	cmd.SetArgs(append([]string{"--project", root, "logs"}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

// #1766: before an org's first run the Live view needs "there is no run yet",
// not monomind's error.
func TestOrgLogsOfAnOrgThatNeverRanSaysNoRun(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	noRunMonomind(t)
	root := t.TempDir()
	writeOrgFile(t, root, "acme.json", `{"name":"acme"}`)

	out, err := runOrgLogs(t, root, "acme")
	if err != nil {
		t.Fatalf("org logs before any run: %v", err)
	}
	var got struct {
		V     int               `json:"v"`
		Org   string            `json:"org"`
		Run   *string           `json:"run"`
		Items []json.RawMessage `json:"items"`
		NoRun bool              `json:"no_run"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.V != 1 || got.Org != "acme" || got.Run != nil || got.Items == nil || len(got.Items) != 0 || !got.NoRun {
		t.Fatalf("output = %s", out)
	}
}

// Everything else monomind refuses stays an error: a run that was asked for by
// id, an org that does not exist, and any other failure.
func TestOrgLogsKeepsOtherFailures(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	noRunMonomind(t)
	root := t.TempDir()
	writeOrgFile(t, root, "acme.json", `{"name":"acme"}`)
	writeOrgFile(t, root, "broken.json", `{"name":"broken"}`)

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"a named run":   {[]string{"acme", "--run", "run-1"}, "no runs found"},
		"no such org":   {[]string{"ghost"}, "no runs found"},
		"other failure": {[]string{"broken"}, "unreadable"},
	} {
		out, err := runOrgLogs(t, root, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, out = %s; want an error naming %q", name, err, out, tc.want)
		}
	}
}
