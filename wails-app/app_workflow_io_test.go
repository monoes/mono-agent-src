package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildTestCLI builds a devaccount CLI into a temp dir so subprocess
// tests exercise the GUI commands independently of the enforcement date. Skips when building is not
// possible in this environment.
func buildTestCLI(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("..") // wails-app/ is nested directly in the repo root
	if _, statErr := os.Stat(filepath.Join(repoRoot, "cmd", "monoagentcli")); err != nil || statErr != nil {
		t.Skipf("repo root with cmd/monoagentcli not found: %v / %v", err, statErr)
	}
	bin := filepath.Join(t.TempDir(), "monoagentcli")
	build := exec.Command("go", "build", "-tags", "devaccount", "-o", bin, "./cmd/monoagentcli")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("building monoagentcli failed (%v): %s", err, out)
	}
	// Keep these subprocess tests about their subject, even after enforcement starts.
	t.Setenv("MONOAGENT_DEV_ENFORCE_FROM", "2999-01-01T00:00:00Z")
	return bin
}

// ─────────────────────────────────────────────────────────────────────────────
// RunNode cancellation (RA1-9)
// ─────────────────────────────────────────────────────────────────────────────

// TestStopNodeRun_KillsRegisteredProcess: StopNodeRun must kill the
// subprocess registered under the run id and complain about runs that are
// unknown or never started.
func TestStopNodeRun_KillsRegisteredProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawned process test uses sleep")
	}
	a := newTestApp(t)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	a.runningCmds["noderun:7"] = cmd

	if err := a.StopNodeRun("7"); err != nil {
		t.Fatalf("StopNodeRun: %v", err)
	}
	if waitErr := cmd.Wait(); waitErr == nil {
		t.Fatal("expected the stopped process to exit with an error, got nil")
	}

	if err := a.StopNodeRun("7"); err == nil {
		t.Fatal("expected error stopping an already-finished run, got nil")
	}

	a.runningCmds["noderun:8"] = &exec.Cmd{} // registered but never started
	if err := a.StopNodeRun("8"); err == nil {
		t.Fatal("expected error for a never-started run, got nil")
	}
}
