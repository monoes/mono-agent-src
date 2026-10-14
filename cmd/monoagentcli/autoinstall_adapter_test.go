package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/autoinstall"
)

// isBuiltinSourceDir is used by TestAutomationValidateBuiltinDir.
var isBuiltinSourceDir = autoinstall.IsBuiltinSourceDir

// TestAutoinstallAdapterWiring pins what autoinstallDeps hands the package: the
// not-found exit code, the reported (single-document) validate failure, and a
// failed install's issues reaching stderr through installResultError.
func TestAutoinstallAdapterWiring(t *testing.T) {
	home := t.TempDir()

	_, _, err := runAutomationCLI(t, home, "automation", "restore", "gemini")
	if exitCodeFor(err) != 2 || !strings.Contains(err.Error(), "library install automation gemini") {
		t.Fatalf("restore: exit %d err %v", exitCodeFor(err), err)
	}

	dir := filepath.Join(home, "bad")
	if err := os.MkdirAll(filepath.Join(dir, "actions"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "automation.json"), []byte(`{"schema":"monoagent.automation/v1","id":"bad-pkg","name":"Bad","version":"0.1.0",
		"site":{"startUrl":"","domains":[]},"permissions":{"steps":["log"],"scripts":[]},
		"actions":["a"],"policy":{"tier":"standard"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "actions", "a.json"), []byte(`{"actionType":"a","automation":"bad-pkg","steps":[{"id":"s","type":"log"}]}`), 0o644)

	out, _, err := runAutomationCLI(t, home, "automation", "validate", dir, "--json")
	var rep reportedError
	if err == nil || exitCodeFor(err) != 1 || !strings.Contains(out, `"ok": false`) || strings.Contains(out, `{"error"`) {
		t.Fatalf("validate: err %v out %s", err, out)
	}
	if !errors.As(err, &rep) {
		t.Fatalf("validate error %T is not a reportedError", err)
	}

	_, errOut, err := runAutomationCLI(t, home, "automation", "install", dir, "--yes")
	if err == nil || !strings.Contains(errOut, "issue(s):") {
		t.Fatalf("install: err %v stderr %s", err, errOut)
	}
	// Nobody can answer on a test's stdin: without --yes the install refuses.
	_, _, err = runAutomationCLI(t, home, "automation", "install", dir)
	if !errors.Is(err, errConfirmationRequired) {
		t.Fatalf("unconfirmed install: %v", err)
	}
}
