package profile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func TestProfileDocumentsGetAndCapture(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	capDir := writeCapture(t, map[string]string{
		"meta.json":      `{"title":"Search Results","url":"https://x.test/","capturedAt":"2026-09-22T11:00:08Z","wordCount":3}`,
		"readable.md":    "# Hello",
		"screenshot.png": "\x89PNG fake",
	})
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO vault_documents (id, seq, filename, path, size_bytes, source, profile_id, url, capture_dir, created_at)
		VALUES ('cap-1', 1, 'page.mhtml', ?, 5, 'capture', 'default', 'https://x.test/', ?, '2026-09-26T10:00:00Z'),
		       ('doc-1', 2, 'cv.pdf', '/nowhere/cv.pdf', 7, 'upload', 'default', NULL, NULL, '2026-09-26T10:00:00Z'),
		       ('other', 3, 'x.pdf', '/nowhere/x.pdf', 1, 'upload', 'someone-else', NULL, NULL, '2026-09-26T10:00:00Z')`,
		filepath.Join(capDir, "page.mhtml"), capDir); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var doc map[string]interface{}
	mustProfileJSON(t, dbPath, &doc, "documents", "get", "doc-1")
	for _, k := range []string{"id", "filename", "path", "size_bytes", "source", "application_id", "created_at", "indexed", "index_error", "stale", "url", "capture_dir", "summary_status"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("documents get missing %q: %v", k, doc)
		}
	}
	if doc["id"] != "doc-1" || doc["filename"] != "cv.pdf" || doc["size_bytes"] != float64(7) {
		t.Errorf("documents get = %v", doc)
	}
	if _, err := runProfileJSON(t, dbPath, "documents", "get", "other"); err == nil || exitCodeOf(t, err) != 2 {
		t.Fatalf("another profile's document: %v", err)
	}

	var v captureView
	mustProfileJSON(t, dbPath, &v, "documents", "capture", "cap-1")
	if v.Title != "Search Results" || v.WordCount != 3 || v.Readable != "# Hello" || !strings.HasPrefix(v.Screenshot, "data:image/png;base64,") {
		t.Fatalf("capture = %+v", v)
	}
	if _, err := runProfileJSON(t, dbPath, "documents", "capture", "doc-1"); err == nil || exitCodeOf(t, err) != 3 {
		t.Fatalf("capture of a non-capture: %v", err)
	}
}
