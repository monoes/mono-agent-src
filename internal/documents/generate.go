package documents

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/vault"
)

// GenerateDocument renders docType against data, saves the HTML and the
// PDF into profileID's vault (optionally linked to applicationID via
// vault.RegisterDocument's variadic parameter), and returns both vault
// IDs. If PDF rendering fails, the already-created HTML document is kept
// (htmlDocID is non-empty) and pdfDocID is empty — see the design spec's
// Error Handling section for why nothing is discarded on a partial failure.
func GenerateDocument(ctx context.Context, db *sql.DB, profileID, applicationID string, docType DocType, data interface{}) (htmlDocID, pdfDocID string, err error) {
	html, err := Render(docType, data)
	if err != nil {
		return "", "", fmt.Errorf("documents.GenerateDocument: %w", err)
	}

	htmlFile, err := os.CreateTemp(scratchDir(), string(docType)+"-*.html")
	if err != nil {
		return "", "", fmt.Errorf("documents.GenerateDocument: creating temp html file: %w", err)
	}
	defer os.Remove(htmlFile.Name())
	if _, err := htmlFile.WriteString(html); err != nil {
		htmlFile.Close()
		return "", "", fmt.Errorf("documents.GenerateDocument: writing temp html file: %w", err)
	}
	htmlFile.Close()

	ctxWithProfile := vault.ContextWithProfileID(ctx, profileID)
	if applicationID != "" {
		htmlDocID, err = vault.RegisterDocument(ctxWithProfile, db, htmlFile.Name(), "generated", applicationID)
	} else {
		htmlDocID, err = vault.RegisterDocument(ctxWithProfile, db, htmlFile.Name(), "generated")
	}
	if err != nil {
		return "", "", fmt.Errorf("documents.GenerateDocument: saving html: %w", err)
	}

	pdfBytes, err := RenderPDFFunc(ctx, html)
	if err != nil {
		return htmlDocID, "", fmt.Errorf("documents.GenerateDocument: rendering pdf: %w", err)
	}

	pdfFile, err := os.CreateTemp(scratchDir(), string(docType)+"-*.pdf")
	if err != nil {
		return htmlDocID, "", fmt.Errorf("documents.GenerateDocument: creating temp pdf file: %w", err)
	}
	defer os.Remove(pdfFile.Name())
	if _, err := pdfFile.Write(pdfBytes); err != nil {
		pdfFile.Close()
		return htmlDocID, "", fmt.Errorf("documents.GenerateDocument: writing temp pdf file: %w", err)
	}
	pdfFile.Close()

	if applicationID != "" {
		pdfDocID, err = vault.RegisterDocument(ctxWithProfile, db, pdfFile.Name(), "generated", applicationID)
	} else {
		pdfDocID, err = vault.RegisterDocument(ctxWithProfile, db, pdfFile.Name(), "generated")
	}
	if err != nil {
		return htmlDocID, "", fmt.Errorf("documents.GenerateDocument: saving pdf: %w", err)
	}

	return htmlDocID, pdfDocID, nil
}
