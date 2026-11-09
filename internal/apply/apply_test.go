// internal/apply/apply_test.go
package apply_test

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/apply"
	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func newTestDB(t *testing.T) *storage.Database {
	t.Helper()
	// apply.Prepare writes generated documents via vault.RegisterDocument,
	// which resolves its path via profiledir.Root — falling back to the real
	// $HOME when the test DB has no profiles.root_dir override. Without
	// this, every run pollutes the developer's actual ~/.monoagent vault.
	testhome.SetHome(t, t.TempDir())
	// The vault syncs each file into the knowledge graph in the background:
	// run a no-op instead of the real monomind, and let it finish before HOME
	// is removed.
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "apply-test.db")
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

func fakeRenderPDF(t *testing.T) {
	t.Helper()
	orig := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) { return []byte("%PDF-fake"), nil }
	t.Cleanup(func() { documents.RenderPDFFunc = orig })
}

func TestPrepareGeneratesDocuments(t *testing.T) {
	fakeRenderPDF(t)
	db := newTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db.DB)
	store := applications.NewStore(db.DB)
	app := &applications.Application{ProfileID: "default", Kind: applications.KindJob, Job: &applications.JobDetails{Title: "Backend Engineer", Company: "Acme", URL: "https://a.example"}}
	if err := store.Create(ctx, app); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cvHTML, cvPDF, letterHTML, letterPDF, err := apply.Prepare(ctx, db.DB, "default", app.ID,
		documents.CVData{Name: "Jane Doe"}, documents.CoverLetterData{SenderName: "Jane Doe"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if cvHTML == "" || cvPDF == "" || letterHTML == "" || letterPDF == "" {
		t.Fatalf("expected all 4 document ids to be set, got %q %q %q %q", cvHTML, cvPDF, letterHTML, letterPDF)
	}

	docs, err := vault.ListDocuments(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 4 {
		t.Fatalf("expected 4 vault documents (cv html+pdf, letter html+pdf), got %d", len(docs))
	}
}

func TestPrepareReusesExistingDocumentsOnSecondCall(t *testing.T) {
	fakeRenderPDF(t)
	db := newTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db.DB)
	store := applications.NewStore(db.DB)
	app := &applications.Application{ProfileID: "default", Kind: applications.KindJob, Job: &applications.JobDetails{Title: "Backend Engineer", Company: "Acme", URL: "https://a.example"}}
	if err := store.Create(ctx, app); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cvData := documents.CVData{Name: "Jane Doe"}
	letterData := documents.CoverLetterData{SenderName: "Jane Doe"}

	id1a, id1b, id1c, id1d, err := apply.Prepare(ctx, db.DB, "default", app.ID, cvData, letterData)
	if err != nil {
		t.Fatalf("Prepare (1st): %v", err)
	}
	id2a, id2b, id2c, id2d, err := apply.Prepare(ctx, db.DB, "default", app.ID, cvData, letterData)
	if err != nil {
		t.Fatalf("Prepare (2nd): %v", err)
	}
	if id1a != id2a || id1b != id2b || id1c != id2c || id1d != id2d {
		t.Fatalf("expected the second Prepare call to reuse the same document ids, got (%s,%s,%s,%s) vs (%s,%s,%s,%s)",
			id1a, id1b, id1c, id1d, id2a, id2b, id2c, id2d)
	}

	docs, err := vault.ListDocuments(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 4 {
		t.Fatalf("expected still only 4 vault documents after a second Prepare call, got %d", len(docs))
	}
}
