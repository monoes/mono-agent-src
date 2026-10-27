package summarycli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

func TestReadOnlyHybridStoreLeavesWorkflowFilesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".monoagent", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "w-legacy.json")
	if err := os.WriteFile(path, []byte(`{"id":"w-legacy","name":"legacy","nodes":[],"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, _ := os.ReadFile(path)
	readOnlyHybridStore(db)
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatalf("the polled store rewrote a workflow file:\n%s\n->\n%s", before, after)
	}
}

// A poll creates nothing: with no workflows folder, none appears.
func TestReadOnlyHybridStoreDoesNotCreateTheWorkflowsFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	readOnlyHybridStore(db)
	if _, err := os.Stat(filepath.Join(home, ".monoagent")); err == nil {
		t.Fatal("a polled store created ~/.monoagent or what is under it")
	}
}
