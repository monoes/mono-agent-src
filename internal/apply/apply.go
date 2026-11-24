// internal/apply/apply.go

// Package apply assembles everything needed to complete one job
// application (generated documents + the posting open in a browser).
// This package itself fills and submits nothing, and has no form
// interaction code. Filling the form is a separate step, the
// application-apply coder task (internal/applyfill, started by
// `application fill start`), which stops before the submit; the submit
// stays with the user. See
// docs/mastermind/specs/2026-09-05-apply-automation-design.md (the
// no-auto-fill decision there is superseded in part, 2026-10-22).
package apply

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/vault"
)

// Prepare ensures applicationID has a generated CV and cover letter in
// the vault, generating them now if none exist yet for this application
// (checked via vault.ListDocuments filtered by ApplicationID) — calling
// Prepare twice on the same application reuses the existing documents
// rather than creating duplicates.
func Prepare(ctx context.Context, db *sql.DB, profileID, applicationID string, cvData documents.CVData, coverLetterData documents.CoverLetterData) (cvHTMLID, cvPDFID, letterHTMLID, letterPDFID string, err error) {
	existing, err := vault.ListDocuments(ctx, db, profileID)
	if err != nil {
		return "", "", "", "", fmt.Errorf("apply.Prepare: listing existing documents: %w", err)
	}

	// A generated document's Source is always "generated" (set by
	// documents.GenerateDocument via vault.RegisterDocument) — Phase 3's
	// manually-uploaded profile documents use Source "upload" and are
	// never mistaken for a prior CV/cover-letter generation here.
	var forApp []vault.DocumentEntry
	for _, d := range existing {
		if d.ApplicationID == applicationID && d.Source == "generated" {
			forApp = append(forApp, d)
		}
	}

	// A generated document's file name starts with its type (GenerateDocument names the temp file so)
	// and ListDocuments is newest-first, so the first HTML and the first PDF of a type are its latest
	// pair. A pair is reused whole: a rerun after a partial failure (the CV made, the letter not)
	// generates only what is missing instead of making the CV again.
	cvHTMLID, cvPDFID = latestPair(forApp, documents.DocTypeCV)
	letterHTMLID, letterPDFID = latestPair(forApp, documents.DocTypeCoverLetter)
	if cvHTMLID == "" || cvPDFID == "" {
		cvHTMLID, cvPDFID, err = documents.GenerateDocument(ctx, db, profileID, applicationID, documents.DocTypeCV, cvData)
		if err != nil {
			return "", "", "", "", fmt.Errorf("apply.Prepare: generating cv: %w", err)
		}
	}
	if letterHTMLID != "" && letterPDFID != "" {
		return cvHTMLID, cvPDFID, letterHTMLID, letterPDFID, nil
	}
	letterHTMLID, letterPDFID, err = documents.GenerateDocument(ctx, db, profileID, applicationID, documents.DocTypeCoverLetter, coverLetterData)
	if err != nil {
		return cvHTMLID, cvPDFID, "", "", fmt.Errorf("apply.Prepare: generating cover letter: %w", err)
	}
	return cvHTMLID, cvPDFID, letterHTMLID, letterPDFID, nil
}

// latestPair is the newest HTML and PDF generated for docType in entries (newest first); either is ""
// when none exists.
func latestPair(entries []vault.DocumentEntry, docType documents.DocType) (htmlID, pdfID string) {
	prefix := string(docType) + "-"
	for _, d := range entries {
		if !strings.HasPrefix(d.Filename, prefix) {
			continue
		}
		switch strings.ToLower(filepath.Ext(d.Filename)) {
		case ".html":
			if htmlID == "" {
				htmlID = d.ID
			}
		case ".pdf":
			if pdfID == "" {
				pdfID = d.ID
			}
		}
	}
	return htmlID, pdfID
}
