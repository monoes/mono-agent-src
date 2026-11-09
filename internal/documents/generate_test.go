package documents_test

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func newGenerateTestDB(t *testing.T) *storage.Database {
	t.Helper()
	// vault.RegisterDocument (called by documents.GenerateDocument) resolves
	// its storage path via profiledir.Root, which falls back to the real
	// $HOME when the test DB has no profiles.root_dir override. Without this,
	// every run of this test writes fake "%PDF-fake" documents into the
	// developer's actual ~/.monoagent/profiles/default/vault/.
	testhome.SetHome(t, t.TempDir())
	// The vault syncs each file into the knowledge graph in the background:
	// run a no-op instead of the real monomind, and let it finish before HOME
	// is removed.
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "documents-generate-test.db")
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

func TestGenerateDocumentCreatesBothVaultEntries(t *testing.T) {
	// Inject a fake RenderPDF so this test doesn't need a real browser —
	// see documents.RenderPDFFunc's doc comment for why it's a swappable var.
	orig := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) {
		return []byte("%PDF-fake"), nil
	}
	t.Cleanup(func() { documents.RenderPDFFunc = orig })

	db := newGenerateTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db.DB)

	htmlID, pdfID, err := documents.GenerateDocument(ctx, db.DB, "default", "app-1", documents.DocTypeCV, documents.CVData{Name: "Jane Doe"})
	if err != nil {
		t.Fatalf("GenerateDocument: %v", err)
	}
	if htmlID == "" || pdfID == "" {
		t.Fatalf("expected both vault ids to be set, got html=%q pdf=%q", htmlID, pdfID)
	}

	docs, err := vault.ListDocuments(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 vault documents (html + pdf), got %d", len(docs))
	}
	for _, d := range docs {
		if d.ApplicationID != "app-1" {
			t.Errorf("expected application_id app-1, got %q for %s", d.ApplicationID, d.ID)
		}
	}
}

func TestGenerateDocumentKeepsHTMLWhenPDFFails(t *testing.T) {
	orig := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) {
		return nil, os.ErrInvalid
	}
	t.Cleanup(func() { documents.RenderPDFFunc = orig })

	db := newGenerateTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db.DB)

	htmlID, pdfID, err := documents.GenerateDocument(ctx, db.DB, "default", "", documents.DocTypeCV, documents.CVData{Name: "Jane Doe"})
	if err == nil {
		t.Fatal("expected an error when PDF rendering fails, got nil")
	}
	if htmlID == "" {
		t.Fatal("expected the HTML document to still be created even though PDF rendering failed")
	}
	if pdfID != "" {
		t.Fatalf("expected empty pdfDocID on PDF failure, got %q", pdfID)
	}
}
