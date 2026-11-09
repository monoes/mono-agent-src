package main

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// parseStrict decodes JSON rejecting unknown fields, so a legacy-key drift
// (e.g. "node_type" instead of "type") fails the test instead of being
// silently ignored.
func parseStrict(data string, v interface{}) error {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ─────────────────────────────────────────────────────────────────────────────
// ExportWorkflow / ImportWorkflow
// ─────────────────────────────────────────────────────────────────────────────

// TestImportWorkflow_RejectsBadInput: garbage input fails validation before
// any subprocess is spawned.
func TestImportWorkflow_RejectsBadInput(t *testing.T) {
	a := newTestApp(t)

	for _, bad := range []string{"", "   ", "not json {", "/nonexistent/path/definitely-not-a-file.json"} {
		if _, err := a.ImportWorkflow(bad); err == nil {
			t.Fatalf("expected error for input %q, got nil", bad)
		}
	}
}

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

// TestExportImportRoundtripViaCLI: GUI save → GUI export → CLI import
// (subprocess, isolated HOME) → GUI read must roundtrip a workflow with its
// nodes, positions, and connections intact. This is the parity gap FD7
// closes: the GUI can now produce and consume the CLI's interchange format.
// Imports are idempotent: on the machine that exported it the workflow comes
// back "unchanged" under its own id; on a fresh machine it is "created" with
// ids preserved, and a changed file re-imported there is "updated" in place.
func TestExportImportRoundtripViaCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME isolation is unix-only")
	}
	cliBin := buildTestCLI(t)

	// Build the CLI before overriding HOME so the go build cache stays on
	// the real home; the import subprocesses below must only see the
	// isolated homes (~/.monoagent of each test sandbox).
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENTCLI_BIN", cliBin)

	a := newTestApp(t)

	saved, err := a.SaveWorkflow(SaveWorkflowRequest{
		Name: "roundtrip", Description: "rt", IsActive: false,
		Nodes: []WorkflowNodeData{
			{ID: "n1", NodeType: "trigger.manual", Name: "Start", PositionX: 3, PositionY: 4, Config: map[string]interface{}{}},
			{ID: "n2", NodeType: "core.set", Name: "Set", Config: map[string]interface{}{"field": "x"}},
		},
		Connections: []WorkflowConnectionData{
			{ID: "c1", SourceNodeID: "n1", TargetNodeID: "n2"},
		},
	})
	if err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}

	exported, err := a.ExportWorkflow(saved.ID)
	if err != nil {
		t.Fatalf("ExportWorkflow: %v", err)
	}

	// Same machine, raw-JSON input mode: already there, nothing changes.
	imp, err := a.ImportWorkflow(exported)
	if err != nil {
		t.Fatalf("ImportWorkflow(json): %v", err)
	}
	if imp.ID != saved.ID || imp.Status != "unchanged" || imp.Name != "roundtrip" {
		t.Fatalf("same-machine re-import = %+v, want id %s status unchanged", imp, saved.ID)
	}

	// Same machine, file-path input mode: same answer.
	wfPath := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(wfPath, []byte(exported), 0o644); err != nil {
		t.Fatalf("writing workflow file: %v", err)
	}
	imp2, err := a.ImportWorkflow(wfPath)
	if err != nil {
		t.Fatalf("ImportWorkflow(path): %v", err)
	}
	if imp2.ID != saved.ID || imp2.Status != "unchanged" {
		t.Fatalf("same-machine path re-import = %+v", imp2)
	}

	// A fresh machine (new HOME and database): created, data intact.
	homeB := t.TempDir()
	testhome.SetHome(t, homeB)
	b := newTestApp(t)
	impB, err := b.ImportWorkflow(exported)
	if err != nil {
		t.Fatalf("ImportWorkflow on fresh home: %v", err)
	}
	if impB.Status != "created" || impB.Name != "roundtrip" {
		t.Fatalf("fresh-home import = %+v", impB)
	}
	got, err := b.GetWorkflow(impB.ID)
	if err != nil {
		t.Fatalf("GetWorkflow(imported): %v", err)
	}
	if got.Name != "roundtrip" || len(got.Nodes) != 2 || len(got.Connections) != 1 {
		t.Fatalf("roundtripped workflow lost data: %+v", got)
	}
	if got.Nodes[0].ID != "n1" || got.Nodes[0].NodeType != "trigger.manual" ||
		got.Nodes[0].PositionX != 3 || got.Nodes[0].PositionY != 4 {
		t.Fatalf("roundtripped node 0 degraded: %+v", got.Nodes[0])
	}
	if got.Connections[0].SourceNodeID != "n1" || got.Connections[0].TargetNodeID != "n2" {
		t.Fatalf("roundtripped connection degraded: %+v", got.Connections[0])
	}

	// The same file, changed, imported there again: updated in place.
	changed := strings.Replace(exported, `"Set"`, `"Set v2"`, 1)
	impB2, err := b.ImportWorkflow(changed)
	if err != nil {
		t.Fatalf("ImportWorkflow(changed): %v", err)
	}
	if impB2.ID != impB.ID || impB2.Status != "updated" {
		t.Fatalf("changed re-import = %+v, want id %s status updated", impB2, impB.ID)
	}
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
