package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// The real binary links the config store, which installs the storage layer's
// write guard: a raw write of a row only the operator may change is refused
// to a caller that is not the operator.
func TestBinaryInstallsTheConfigWriteGuard(t *testing.T) {
	if storage.ConfigWriteGuard == nil {
		t.Fatal("storage.ConfigWriteGuard is not installed in the binary")
	}
	t.Setenv("MONOAGENT_WORKFLOW_RUN", "wf-1")
	if err := storage.ConfigWriteGuard("workflow.error_workflow"); err == nil {
		t.Error("a workflow run may write a reserved row through storage")
	}
	if err := storage.ConfigWriteGuard("linkedin_login_x"); err != nil {
		t.Errorf("an ordinary row: %v", err)
	}
}
