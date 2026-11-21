package httpapi

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
)

// The standalone runtime stamps ownerless workflow files when it opens its
// file store.
func TestNewRuntimeStampsOwnerlessWorkflowFiles(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "wf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "w-legacy.json")
	if err := os.WriteFile(path, []byte(`{"id":"w-legacy","name":"legacy","nodes":[],"connections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "h.db")
	testdb.MustSeed(dbPath) // has the profile 'default' the file is stamped with
	rt, err := newRuntime(Options{DBPath: dbPath, WorkflowsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	b, _ := os.ReadFile(path)
	var doc struct {
		ProfileID string `json:"profile_id"`
	}
	_ = json.Unmarshal(b, &doc)
	if doc.ProfileID != "default" {
		t.Fatalf("file owner = %q, want default", doc.ProfileID)
	}
}
