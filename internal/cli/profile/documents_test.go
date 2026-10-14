// internal/cli/profile/documents_test.go
package profile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setFakeMonomindOnPathCLI(t *testing.T) {
	t.Helper()
	fakeDir := t.TempDir()
	src, err := filepath.Abs("../../monomind/testdata/fake-monomind.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, filepath.Join(fakeDir, "monomind")); err != nil {
		t.Fatalf("symlink fake monomind onto PATH: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runProfileCmd(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &testConfig{DBPath: dbPath, JSONOutput: true}
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

// runProfileCmdText is runProfileCmd's JSONOutput:false counterpart, for
// exercising the plain-text table rendering path — mirrors how
// secret_test.go pairs runSecretCmd/runSecretCmdText.
func runProfileCmdText(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &testConfig{DBPath: dbPath, JSONOutput: false}
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestProfileUploadListDeleteDocument(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)

	docPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	uploadOut, err := runProfileCmd(t, dbPath, "upload-document", docPath)
	if err != nil {
		t.Fatalf("upload-document: %v (%s)", err, uploadOut)
	}
	if !strings.Contains(uploadOut, `"id"`) {
		t.Fatalf("expected JSON id in output, got: %s", uploadOut)
	}

	listOut, err := runProfileCmd(t, dbPath, "documents", "list")
	if err != nil {
		t.Fatalf("documents list: %v", err)
	}
	if !strings.Contains(listOut, "resume.txt") {
		t.Fatalf("expected filename in list output, got: %s", listOut)
	}
}

// TestProfileDocumentsListTableTruncatesLongFilename guards against the
// tablewriter v1 migration regression where `documents list`'s FILENAME
// column lost its old wrap-based readability (see the identical concern in
// application_test.go's TestApplicationListTableTruncatesLongValues) without
// gaining truncation to compensate.
func TestProfileDocumentsListTableTruncatesLongFilename(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)

	longName := "quarterly-performance-review-and-career-development-planning-notes-final-v3.txt"
	docPath := filepath.Join(t.TempDir(), longName)
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	uploadOut, err := runProfileCmd(t, dbPath, "upload-document", docPath)
	if err != nil {
		t.Fatalf("upload-document: %v (%s)", err, uploadOut)
	}

	listOut, err := runProfileCmdText(t, dbPath, "documents", "list")
	if err != nil {
		t.Fatalf("documents list: %v", err)
	}
	if strings.Contains(listOut, longName) {
		t.Fatalf("expected the long filename to be truncated in table output, got the full string unbroken: %s", listOut)
	}
	if !strings.Contains(listOut, "...") {
		t.Fatalf("expected a truncation marker in table output, got: %s", listOut)
	}
}

func TestProfileUploadDocumentRecordsIndexedStatus(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)

	docPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	uploadOut, err := runProfileCmd(t, dbPath, "upload-document", docPath)
	if err != nil {
		t.Fatalf("upload-document: %v (%s)", err, uploadOut)
	}
	if !strings.Contains(uploadOut, `"indexed": true`) {
		t.Fatalf("expected indexed:true in output, got: %s", uploadOut)
	}

	listOut, err := runProfileCmd(t, dbPath, "documents", "list")
	if err != nil {
		t.Fatalf("documents list: %v", err)
	}
	if !strings.Contains(listOut, `"Indexed": true`) {
		t.Fatalf("expected Indexed:true in list output, got: %s", listOut)
	}
}

func TestProfileUploadDocumentRecordsIndexingFailure(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	t.Setenv("INGEST_FAIL", "1")
	dbPath := newProfileDocsCLITestDB(t)

	docPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	uploadOut, err := runProfileCmd(t, dbPath, "upload-document", docPath)
	if err != nil {
		t.Fatalf("upload-document should still succeed even when indexing fails: %v (%s)", err, uploadOut)
	}
	if !strings.Contains(uploadOut, `"indexed": false`) {
		t.Fatalf("expected indexed:false in output, got: %s", uploadOut)
	}
	if !strings.Contains(uploadOut, `"index_error"`) {
		t.Fatalf("expected index_error in output, got: %s", uploadOut)
	}

	listOut, err := runProfileCmd(t, dbPath, "documents", "list")
	if err != nil {
		t.Fatalf("documents list: %v", err)
	}
	if !strings.Contains(listOut, `"Indexed": false`) {
		t.Fatalf("expected Indexed:false in list output, got: %s", listOut)
	}
	if !strings.Contains(listOut, `"IndexError"`) {
		t.Fatalf("expected IndexError in list output, got: %s", listOut)
	}
}

func TestProfileSearchKnowledge(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)

	out, err := runProfileCmd(t, dbPath, "search-knowledge", "backend engineer")
	if err != nil {
		t.Fatalf("search-knowledge: %v (%s)", err, out)
	}
	if !strings.Contains(out, "distributed systems") {
		t.Fatalf("expected excerpt text in output, got: %s", out)
	}
}

func uploadDocWithoutIndexing(t *testing.T, dbPath, docPath string) string {
	t.Helper()
	// Upload with a broken PATH (no monomind) so it lands Not indexed --
	// the "index <id>" command under test is what performs the real
	// ingest attempt, not upload-document.
	t.Setenv("PATH", t.TempDir()) // deliberately empty, no monomind binary
	uploadOut, err := runProfileCmd(t, dbPath, "upload-document", docPath)
	if err != nil {
		t.Fatalf("upload-document: %v (%s)", err, uploadOut)
	}
	id := extractJSONStringField(t, uploadOut, "id")
	if id == "" {
		t.Fatalf("expected an id in upload-document output, got: %s", uploadOut)
	}
	return id
}

func extractJSONStringField(t *testing.T, jsonOut, field string) string {
	t.Helper()
	marker := `"` + field + `": "`
	i := strings.Index(jsonOut, marker)
	if i < 0 {
		return ""
	}
	rest := jsonOut[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func TestProfileDocumentsIndexSuccess(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	docPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	id := uploadDocWithoutIndexing(t, dbPath, docPath)

	setFakeMonomindOnPathCLI(t)
	indexOut, err := runProfileCmd(t, dbPath, "documents", "index", id)
	if err != nil {
		t.Fatalf("documents index: %v (%s)", err, indexOut)
	}
	if !strings.Contains(indexOut, `"indexed": true`) {
		t.Fatalf("expected indexed:true, got: %s", indexOut)
	}

	listOut, err := runProfileCmd(t, dbPath, "documents", "list")
	if err != nil {
		t.Fatalf("documents list: %v (%s)", err, listOut)
	}
	if !strings.Contains(listOut, `"Indexed": true`) {
		t.Fatalf("expected Indexed:true in list output, got: %s", listOut)
	}
	if !strings.Contains(listOut, `"Stale": false`) {
		t.Fatalf("expected Stale:false immediately after indexing, got: %s", listOut)
	}
}

func TestProfileDocumentsIndexRecordsFailure(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	docPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	id := uploadDocWithoutIndexing(t, dbPath, docPath)

	setFakeMonomindOnPathCLI(t)
	t.Setenv("INGEST_FAIL", "1")
	indexOut, err := runProfileCmd(t, dbPath, "documents", "index", id)
	if err != nil {
		t.Fatalf("documents index should still succeed even when indexing fails: %v (%s)", err, indexOut)
	}
	if !strings.Contains(indexOut, `"indexed": false`) {
		t.Fatalf("expected indexed:false, got: %s", indexOut)
	}
	if !strings.Contains(indexOut, `"index_error"`) {
		t.Fatalf("expected index_error, got: %s", indexOut)
	}
}

func TestProfileDocumentsIndexUnknownID(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)

	_, err := runProfileCmd(t, dbPath, "documents", "index", "doc-does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown document id")
	}
}

func TestProfileDocumentsIndexPreservesPriorSuccessOnLaterFailure(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	docPath := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(docPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	id := uploadDocWithoutIndexing(t, dbPath, docPath)

	setFakeMonomindOnPathCLI(t)
	if _, err := runProfileCmd(t, dbPath, "documents", "index", id); err != nil {
		t.Fatalf("first index: %v", err)
	}

	t.Setenv("INGEST_FAIL", "1")
	if _, err := runProfileCmd(t, dbPath, "documents", "index", id); err != nil {
		t.Fatalf("second (failing) index: %v", err)
	}

	listOut, err := runProfileCmd(t, dbPath, "documents", "list")
	if err != nil {
		t.Fatalf("documents list: %v (%s)", err, listOut)
	}
	if !strings.Contains(listOut, `"Indexed": true`) {
		t.Fatalf("expected the prior success to survive a later failed re-index, got: %s", listOut)
	}
	if !strings.Contains(listOut, `"IndexError"`) {
		t.Fatalf("expected the new failure's IndexError to be recorded, got: %s", listOut)
	}
}
