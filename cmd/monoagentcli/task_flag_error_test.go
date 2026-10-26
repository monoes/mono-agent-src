package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// An unknown flag of a task command is the parser's exit 1 with {"error"} and no code,
// like every other command group: the convention is held here so that nobody gives the
// task group its own, and `ref tasks` says the same.
func TestTaskUnknownFlagIsExit1WithNoCode(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "task", "list", "--bogus"},
		{"--json", "task", "session", "new", "--bogus"},
		{"--json", "workflow", "list", "--bogus"},
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
		if code := exitCodeFor(err); code != 1 {
			t.Errorf("%v: exit %d, want 1", args, code)
		}
		var stdout, stderr bytes.Buffer
		reportCommandError(args, err, &stdout, &stderr)
		var doc map[string]any
		if e := json.Unmarshal(stdout.Bytes(), &doc); e != nil {
			t.Fatalf("%v: stdout %q: %v", args, stdout.String(), e)
		}
		if _, has := doc["code"]; has || !strings.Contains(doc["error"].(string), "unknown flag") {
			t.Errorf("%v: %v", args, doc)
		}
	}
}
