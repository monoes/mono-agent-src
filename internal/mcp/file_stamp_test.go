package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

func ownerlessFileDir(t *testing.T) (dir, path string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "wf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "w-legacy.json")
	if err := os.WriteFile(path, []byte(`{"id":"w-legacy","name":"legacy","nodes":[],"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func fileOwner(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ProfileID string `json:"profile_id"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.ProfileID
}

// An ownerless workflow file is stamped when the MCP runtime builds its file
// store, so it is visible without any CLI command having run first.
func TestNewRuntimeStampsOwnerlessWorkflowFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, path := ownerlessFileDir(t)
	rt, err := newRuntime(Options{DBPath: filepath.Join(t.TempDir(), "m.db"), WorkflowsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if got := fileOwner(t, path); got != "default" {
		t.Fatalf("file owner = %q, want default", got)
	}
}

func TestGrantRuntimeStampsOwnerlessWorkflowFiles(t *testing.T) {
	dir, path := ownerlessFileDir(t)
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := grantRuntime(db, Options{WorkflowsDir: dir}); err != nil {
		t.Fatal(err)
	}
	if got := fileOwner(t, path); got != "default" {
		t.Fatalf("file owner = %q, want default", got)
	}
}
