package documentsnodes_test

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/documents"
	documentsnodes "github.com/monoes/mono-agent/internal/nodes/documents"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func newTestDB(t *testing.T) *storage.Database {
	t.Helper()
	// documents.render writes generated documents via vault.RegisterDocument,
	// which resolves its path via profiledir.Root — falling back to the real
	// $HOME when the test DB has no profiles.root_dir override. Without
	// this, every run pollutes the developer's actual ~/.monoagent vault.
	testhome.SetHome(t, t.TempDir())
	// The vault syncs each file into the knowledge graph in the background:
	// run a no-op instead of the real monomind, and let it finish before HOME
	// is removed.
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "documents-node-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRenderNodeCreatesDocument(t *testing.T) {
	orig := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) { return []byte("%PDF-fake"), nil }
	t.Cleanup(func() { documents.RenderPDFFunc = orig })

	db := newTestDB(t)
	documentsnodes.SetGlobalDB(db.DB)

	node := &documentsnodes.RenderNode{}
	if node.Type() != "documents.render" {
		t.Fatalf("expected type documents.render, got %q", node.Type())
	}
	config := map[string]interface{}{
		"doc_type": "cv",
		"data":     map[string]interface{}{"name": "Jane Doe"},
	}
	outputs, err := node.Execute(vault.ContextWithProfileID(context.Background(), "default"), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(outputs) != 1 || len(outputs[0].Items) != 1 {
		t.Fatalf("expected exactly one output item, got %+v", outputs)
	}
	if outputs[0].Items[0].JSON["html_document_id"] == "" {
		t.Fatal("expected html_document_id in output")
	}
}

func TestRenderNodeRejectsUnknownDocType(t *testing.T) {
	db := newTestDB(t)
	documentsnodes.SetGlobalDB(db.DB)

	node := &documentsnodes.RenderNode{}
	config := map[string]interface{}{"doc_type": "nonsense", "data": map[string]interface{}{}}
	if _, err := node.Execute(vault.ContextWithProfileID(context.Background(), "default"), workflow.NodeInput{}, config); err == nil {
		t.Fatal("expected error for unknown doc_type, got nil")
	}
}
