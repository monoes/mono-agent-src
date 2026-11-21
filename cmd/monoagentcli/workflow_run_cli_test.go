package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// `workflow run` and `templates run` through the CLI's root: its flags, its exit codes and its
// --json error document (the commands live in internal/cli/workflowrun).

// `templates run find_jobs` with no --input stops before building anything.
func TestTemplatesRunWithoutInputFailsFast(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--profile", "default", "workflow", "templates", "run", "find_jobs", "--db-path", filepath.Join(t.TempDir(), "m.db")})
	err := root.Execute()
	if err == nil || !strings.HasPrefix(err.Error(), "missing input: keywords") || exitCodeFor(err) != 3 {
		t.Fatalf("err = %v (exit %d)", err, exitCodeFor(err))
	}
}

func TestWorkflowRunUnknownIDSaysNotFound(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--profile", "default", "workflow", "run", "nope", "--db-path", seededDBPath(t, "m.db")})
	err := root.Execute()
	want := `workflow "nope" not found — list them with: monoagentcli workflow list`
	if err == nil || err.Error() != want || exitCodeFor(err) != 2 {
		t.Fatalf("err = %v (exit %d), want %q (exit 2)", err, exitCodeFor(err), want)
	}

	// With --json the same failure is one {"error","code","hint"} document.
	var stdout, stderr bytes.Buffer
	reportCommandError([]string{"--json", "workflow", "run", "nope"}, err, &stdout, &stderr)
	var doc map[string]string
	if json.Unmarshal(stdout.Bytes(), &doc) != nil || doc["error"] != want || doc["code"] != "not_found" ||
		doc["hint"] != "list them with: monoagentcli workflow list" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.TrimSpace(stderr.String()) != want {
		t.Errorf("stderr = %q", stderr.String())
	}
}
