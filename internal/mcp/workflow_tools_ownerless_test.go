package mcp

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// An ownerless workflow belongs to nobody: it is not served as a tool, and the
// note of an owned one always names its profile.
func TestOwnerlessWorkflowIsNotServedAsATool(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "wf")
	rt, err := newRuntime(Options{DBPath: filepath.Join(t.TempDir(), "m.db"), WorkflowsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	// Written after the runtime stamped the directory, so it stays ownerless.
	if err := os.WriteFile(filepath.Join(dir, "w-legacy.json"), []byte(`{"id":"w-legacy","name":"legacy","is_active":true,"nodes":[],"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	wfs, err := listAllProfilesWorkflows(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range wfs {
		if wf.ID == "w-legacy" {
			t.Fatalf("ownerless workflow listed: %+v", wf)
		}
	}
	note := workflowToolProfileNote(&workflow.Workflow{ID: "x", ProfileID: "work"})
	if !strings.Contains(note, "belongs to profile work") {
		t.Errorf("note = %q", note)
	}
}
