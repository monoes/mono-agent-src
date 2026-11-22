package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportWorkflowDoesNotReadFilesFromDisk(t *testing.T) {
	a := newTestApp(t)
	p := filepath.Join(t.TempDir(), "wf.json")
	if err := os.WriteFile(p, []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := a.ImportWorkflow(p)
	if err == nil || !strings.Contains(err.Error(), "valid workflow JSON") {
		t.Fatalf("a file path was accepted: %v", err)
	}
}
