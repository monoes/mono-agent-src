package workflowio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The one-off DB-to-files migration reports an existing file as already there, via the same check
// it uses before writing; it must never be the thing that overwrites a canonical file.
func TestMigrateSeesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := files.ReadRawForMigrate("w1"); exists {
		t.Fatal("no file yet")
	}
	if err := os.WriteFile(filepath.Join(dir, "w1.json"), []byte(`{"id":"w1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := files.ReadRawForMigrate("w1"); !exists {
		t.Fatal("the existing file is not seen")
	}
}
