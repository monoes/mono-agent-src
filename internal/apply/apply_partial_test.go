package apply_test

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/apply"
	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/vault"
)

// A rerun after the cover letter failed keeps the CV that was made and generates only the letter (R10-09).
func TestPrepareAfterAPartialFailureDoesNotRegenerateTheCV(t *testing.T) {
	db := newTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db.DB)
	store := applications.NewStore(db.DB)
	app := &applications.Application{ProfileID: "default", Kind: applications.KindJob, Job: &applications.JobDetails{Title: "Backend Engineer", Company: "Acme", URL: "https://a.example"}}
	if err := store.Create(ctx, app); err != nil {
		t.Fatalf("Create: %v", err)
	}
	orig := documents.RenderPDFFunc
	t.Cleanup(func() { documents.RenderPDFFunc = orig })
	calls := 0
	documents.RenderPDFFunc = func(context.Context, string) ([]byte, error) {
		calls++
		if calls == 2 { // the letter's PDF
			return nil, errors.New("renderer down")
		}
		return []byte("%PDF-fake"), nil
	}
	cvHTML, cvPDF, _, _, err := apply.Prepare(ctx, db.DB, "default", app.ID, documents.CVData{Name: "Jane"}, documents.CoverLetterData{SenderName: "Jane"})
	if err == nil {
		t.Fatal("the first Prepare should have failed on the letter")
	}
	h2, p2, lh, lp, err := apply.Prepare(ctx, db.DB, "default", app.ID, documents.CVData{Name: "Jane"}, documents.CoverLetterData{SenderName: "Jane"})
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if h2 != cvHTML || p2 != cvPDF || lh == "" || lp == "" {
		t.Fatalf("the CV was made again: first %s/%s, second %s/%s, letter %s/%s", cvHTML, cvPDF, h2, p2, lh, lp)
	}
	docs, _ := vault.ListDocuments(ctx, db.DB, "default")
	if len(docs) != 5 { // cv html+pdf, the failed letter's html, the letter's html+pdf
		t.Fatalf("%d documents, want 5", len(docs))
	}
}
