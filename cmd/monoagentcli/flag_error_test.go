package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A flag the parser refuses (an unknown one, a value that does not parse) is invalid input in every
// command group: exit 3 and {"error","code":"invalid_input"} with --json. One hook on the root
// command does it, so no group gives itself another convention, and `ref tasks` says the same.
func TestUnknownFlagIsInvalidInputInEveryGroup(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "task", "list", "--bogus"},
		{"--json", "task", "session", "new", "--bogus"},
		{"--json", "workflow", "list", "--bogus"},
		{"--json", "api", "status", "--bogus"},
		{"--json", "node", "list", "--bogus"},
		{"--json", "node", "run", "--bogus"},
		{"--json", "task", "list", "--limit", "many"},
	} {
		root := newRootCmd()
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Fatalf("%v: no error", args)
		}
		if code := exitCodeFor(err); code != 3 {
			t.Errorf("%v: exit %d, want 3", args, code)
		}
		var stdout, stderr bytes.Buffer
		reportCommandError(args, err, &stdout, &stderr)
		var doc map[string]any
		if e := json.Unmarshal(stdout.Bytes(), &doc); e != nil {
			t.Fatalf("%v: stdout %q: %v", args, stdout.String(), e)
		}
		if doc["code"] != "invalid_input" || doc["error"] == "" {
			t.Errorf("%v: %v", args, doc)
		}
	}
}
