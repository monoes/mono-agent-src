package attachments

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveWritesFileUnderMessageDir(t *testing.T) {
	testhome.SetHome(t, t.TempDir())

	path, err := Save("AAMkAGI2=", "invoice.pdf", []byte("%PDF-1.4 fake"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("attachment not readable at returned path: %v", err)
	}
	if string(data) != "%PDF-1.4 fake" {
		t.Errorf("content mismatch: %q", data)
	}
	if filepath.Base(path) != "invoice.pdf" {
		t.Errorf("filename not preserved: %s", filepath.Base(path))
	}
	if !strings.HasPrefix(path, Dir()) {
		t.Errorf("path %s is outside the attachments dir %s", path, Dir())
	}
}

// A hostile filename must not escape the message directory.
func TestSaveRejectsPathTraversal(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)

	path, err := Save("msg1", "../../../../etc/passwd", []byte("pwned"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(Dir(), "msg1")+string(os.PathSeparator)) {
		t.Fatalf("traversal escaped the message dir: %s", path)
	}
	if _, err := os.Stat(filepath.Join(home, "etc", "passwd")); err == nil {
		t.Fatal("wrote outside the attachments directory")
	}
}

// Re-syncing the same message must overwrite, not accumulate copies.
func TestSaveIsIdempotentPerMessage(t *testing.T) {
	testhome.SetHome(t, t.TempDir())

	first, err := Save("msg1", "report.txt", []byte("v1"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, err := Save("msg1", "report.txt", []byte("v2"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if first != second {
		t.Errorf("same message+filename produced two paths: %s vs %s", first, second)
	}
	entries, err := os.ReadDir(filepath.Dir(first))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 file after re-sync, got %d", len(entries))
	}
}

// A filename that needs rewriting (spaces are not allowed in the path
// segment) must keep its extension, so a PDF reader or an AI agent that
// dispatches on file extension still recognizes the file. Found live against
// a real Outlook attachment ("Verto Overview.pdf") during manual testing.
func TestSaveRewrittenFilenameKeepsExtension(t *testing.T) {
	testhome.SetHome(t, t.TempDir())

	path, err := Save("msg1", "Verto Overview.pdf", []byte("%PDF-1.4"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := filepath.Ext(path); got != ".pdf" {
		t.Errorf("rewritten filename lost its extension: %s (ext=%q)", path, got)
	}
}

// Two different messages that sanitize to the same segment must not collide.
func TestSaveDistinguishesSimilarMessageIDs(t *testing.T) {
	testhome.SetHome(t, t.TempDir())

	a, err := Save("id/one", "f.txt", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Save("id+one", "f.txt", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(a) == filepath.Dir(b) {
		t.Errorf("distinct message ids shared a directory: %s", filepath.Dir(a))
	}
}
