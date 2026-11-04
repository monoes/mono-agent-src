//go:build !windows

package testhome

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const helperEnv = "TESTHOME_GUARD_HELPER"

// The helper runs in a child test binary: the guard's verdict is the child's exit status.
func TestServiceManagerGuardHelper(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		t.Skip("runs only as the child of the guard tests")
	case "real":
		// a test that reaches the service manager with nothing of its own in front
		_ = exec.Command("systemctl", "--user", "restart", "monoagent-daemon").Run()
	case "own-stub":
		// a test that stubs the service manager itself, before the guard
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := exec.Command("systemctl", "--user", "restart", "x").Run(); err != nil {
			t.Fatal(err)
		}
	case "untouched":
	}
}

func runHelper(t *testing.T, mode string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestServiceManagerGuardHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestARunThatCallsARealServiceManagerFails(t *testing.T) {
	out, err := runHelper(t, "real")
	if err == nil {
		t.Fatalf("the run passed though a test called systemctl:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user restart monoagent-daemon") {
		t.Fatalf("the call is not reported:\n%s", out)
	}
}

func TestATestWithItsOwnServiceManagerStubCoexistsWithTheGuard(t *testing.T) {
	if out, err := runHelper(t, "own-stub"); err != nil {
		t.Fatalf("a test with its own stub failed: %v\n%s", err, out)
	}
}

func TestARunThatCallsNoServiceManagerPasses(t *testing.T) {
	if out, err := runHelper(t, "untouched"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The guard directory is first on PATH in every test binary that calls Main.
func TestTheGuardStubsAreFirstOnPath(t *testing.T) {
	for _, name := range serviceManagers {
		p, err := exec.LookPath(name)
		if err != nil || !strings.Contains(p, "monoagent-noservice-") {
			t.Errorf("%s resolves to %q (%v), want the guard's stub", name, p, err)
		}
	}
}
