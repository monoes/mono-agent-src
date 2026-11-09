// internal/nodes/apply/prepare_test.go
package applynodes_test

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/documents"
	applynodes "github.com/monoes/mono-agent/internal/nodes/apply"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func newTestDB(t *testing.T) *storage.Database {
	t.Helper()
	// applications.prepare (via apply.Prepare) writes documents through
	// vault.RegisterDocument, which resolves its path via profiledir.Root —
	// falling back to the real $HOME when the test DB has no
	// profiles.root_dir override. Without this, every run pollutes the
	// developer's actual ~/.monoagent vault.
	testhome.SetHome(t, t.TempDir())
	// The vault syncs each file into the knowledge graph in the background:
	// run a no-op instead of the real monomind, and let it finish before HOME
	// is removed.
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "apply-node-test.db")
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

func TestPrepareNodeGeneratesDocuments(t *testing.T) {
	origPDF := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) { return []byte("%PDF-fake"), nil }
	t.Cleanup(func() { documents.RenderPDFFunc = origPDF })

	db := newTestDB(t)
	applynodes.SetGlobalDB(db.DB)
	store := applications.NewStore(db.DB)
	app := &applications.Application{ProfileID: "default", Kind: applications.KindJob, Job: &applications.JobDetails{Title: "Backend Engineer", Company: "Acme", URL: "https://a.example"}}
	if err := store.Create(context.Background(), app); err != nil {
		t.Fatalf("Create: %v", err)
	}

	node := &applynodes.PrepareNode{}
	if node.Type() != "applications.prepare" {
		t.Fatalf("expected type applications.prepare, got %q", node.Type())
	}
	config := map[string]interface{}{
		"application_id":    app.ID,
		"cv_data":           map[string]interface{}{"name": "Jane Doe"},
		"cover_letter_data": map[string]interface{}{"senderName": "Jane Doe"},
	}
	outputs, err := node.Execute(vault.ContextWithProfileID(context.Background(), "default"), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outputs[0].Items[0].JSON["cv_pdf_document_id"] == "" {
		t.Fatal("expected cv_pdf_document_id in output")
	}
}
