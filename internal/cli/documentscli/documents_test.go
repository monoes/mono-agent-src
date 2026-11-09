package documentscli

import (
	"bytes"
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func newDocumentsCLITestDB(t *testing.T) string {
	t.Helper()
	// "documents render" writes generated documents via
	// vault.RegisterDocument, which resolves its path via profiledir.Root —
	// falling back to the real $HOME when the test DB has no
	// profiles.root_dir override. Without this, every run pollutes the
	// developer's actual ~/.monoagent vault.
	testhome.SetHome(t, t.TempDir())
	// The vault syncs each file into the knowledge graph in the background;
	// let it finish before HOME is removed. (MONOMIND_BIN is left alone:
	// tests sharing this helper put their own fake monomind on PATH.)
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "cli-documents-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

func runDocumentsCmd(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(Deps{
		JSONOutput: func() bool { return true },
		ProfileID:  func() string { return "default" },
		OpenDB:     func() (*storage.Database, error) { return storage.NewDatabase(dbPath) },
	})
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestDocumentsRenderCV(t *testing.T) {
	orig := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) { return []byte("%PDF-fake"), nil }
	t.Cleanup(func() { documents.RenderPDFFunc = orig })

	dbPath := newDocumentsCLITestDB(t)
	dataFile := filepath.Join(t.TempDir(), "cv.json")
	if err := os.WriteFile(dataFile, []byte(`{"name":"Jane Doe","title":"Backend Engineer"}`), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := runDocumentsCmd(t, dbPath, "render", "--type", "cv", "--data-file", dataFile)
	if err != nil {
		t.Fatalf("documents render: %v (%s)", err, out)
	}
	if !strings.Contains(out, "html_document_id") || !strings.Contains(out, "pdf_document_id") {
		t.Fatalf("expected both document ids in output, got: %s", out)
	}
}

func TestDocumentsRenderRejectsUnknownType(t *testing.T) {
	dbPath := newDocumentsCLITestDB(t)
	dataFile := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(dataFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runDocumentsCmd(t, dbPath, "render", "--type", "nonsense", "--data-file", dataFile); err == nil {
		t.Fatal("expected error for unknown --type, got nil")
	}
}

// A data file that is missing is a plain failure; one that does not parse as the type's data is
// invalid input (exit 3); both are found before anything is generated.
func TestDocumentsRenderRefusesBadData(t *testing.T) {
	dbPath := newDocumentsCLITestDB(t)
	if _, err := runDocumentsCmd(t, dbPath, "render", "--type", "cv", "--data-file", filepath.Join(t.TempDir(), "absent.json")); err == nil || !strings.HasPrefix(err.Error(), "reading data file: ") {
		t.Errorf("missing file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"name":`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"cv", "cover_letter", "tender_proposal"} {
		_, err := runDocumentsCmd(t, dbPath, "render", "--type", typ, "--data-file", bad)
		if err == nil || !strings.HasPrefix(err.Error(), "parsing --data-file: ") || clicore.ExitCodeFor(err) != 3 {
			t.Errorf("%s: %v", typ, err)
		}
	}
}

func TestDocumentsRenderNeedsTypeAndDataFile(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(Deps{})
	render, _, err := cmd.Find([]string{"render"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"type", "data-file"} {
		f := render.Flags().Lookup(name)
		if f == nil || len(f.Annotations[cobra.BashCompOneRequiredFlag]) == 0 {
			t.Errorf("--%s is not required", name)
		}
	}
	if render.Flags().Lookup("application-id") == nil {
		t.Error("no --application-id")
	}
}
