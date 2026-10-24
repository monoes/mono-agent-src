//go:build !windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testHash = "sha256:abc12345"

func TestAITaskApplyBindingsRunTheExactCommands(t *testing.T) {
	a, log := aitaskApp(t, `echo '{"ok":true,"workflowId":"w1"}'`+"\n")
	apply := func(id string) (string, error) { return a.ApplyFailedRunFix(id, false, testHash) }
	for _, call := range []func(string) (string, error){a.PreviewFailedRunFix, apply, a.SaveGeneratedWorkflow} {
		got, err := call("task-1")
		if err != nil || got != `{"ok":true,"workflowId":"w1"}` {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if _, err := a.ApplyFailedRunFix("task-1", true, testHash); err != nil {
		t.Fatal(err)
	}
	want := "--profile work --json workflow apply-fix --dry-run -- task-1\n--profile work --json workflow apply-fix --expect-hash sha256:abc12345 -- task-1\n--profile work --json workflow save-draft -- task-1\n--profile work --json workflow apply-fix --allow-active --expect-hash sha256:abc12345 -- task-1"
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != want {
		t.Fatalf("CLI calls = %v", calls)
	}
}

func TestAITaskApplyBindingsKeepTheCLIsRefusal(t *testing.T) {
	a, _ := aitaskApp(t, `echo '{"error":"the workflow changed since the fix was proposed","code":"stale"}'; exit 1`+"\n")
	_, err := a.ApplyFailedRunFix("task-1", false, testHash)
	if err == nil || err.Error() != "stale: the workflow changed since the fix was proposed" {
		t.Fatalf("err = %v", err)
	}
}

func TestAITaskApplyBindingsRefuseAHostileIDBeforeTheCLI(t *testing.T) {
	a, log := aitaskApp(t, "echo '{}'\n")
	for _, id := range []string{"", "--all", "-x", "a b", "a;rm", "../x", strings.Repeat("a", 65)} {
		if _, err := a.ApplyFailedRunFix(id, true, testHash); err == nil {
			t.Errorf("id %q was accepted", id)
		}
		if _, err := a.PreviewFailedRunFix(id); err == nil {
			t.Errorf("preview id %q was accepted", id)
		}
		if _, err := a.SaveGeneratedWorkflow(id); err == nil {
			t.Errorf("save id %q was accepted", id)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatalf("CLI was run: %v", loggedArgs(t, log))
	}
}

// The apply and save bindings live in the Wails app only: no HTTP agent API,
// extension bridge or CLI code may name them.
func TestAITaskApplyBindingsAreNotExposedElsewhere(t *testing.T) {
	for _, root := range []string{"../internal", "../cmd", "../chrome-extension"} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.Contains(p, "node_modules") {
				return nil
			}
			if b, rerr := os.ReadFile(p); rerr == nil && (strings.Contains(string(b), "ApplyFailedRunFix") || strings.Contains(string(b), "SaveGeneratedWorkflow")) {
				t.Errorf("%s names an apply/save binding", p)
			}
			return nil
		})
	}
}

func TestApplyFailedRunFixRefusesAMissingOrOddHashBeforeTheCLI(t *testing.T) {
	a, log := aitaskApp(t, "echo '{}'\n")
	for _, h := range []string{"", "short", "--allow-active", "a b c d e f g h", "x;y;z;w;v;u", strings.Repeat("a", 129)} {
		if _, err := a.ApplyFailedRunFix("task-1", false, h); err == nil {
			t.Errorf("hash %q was accepted", h)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatalf("CLI was run: %v", loggedArgs(t, log))
	}
}
