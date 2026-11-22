package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func runDocs(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(append([]string{"documents"}, args...))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

// discoveredFixture is a profile "work" with two scanned documents.
func discoveredFixture(t *testing.T) (cfg *testConfig, root string) {
	t.Helper()
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)
	seedProfiles(t, dbPath, "work")
	home, _ := os.UserHomeDir()
	root = filepath.Join(home, ".monoagent", "profiles", "work")
	writeFile(t, filepath.Join(root, "career-profile.md"), "career")
	writeFile(t, filepath.Join(root, "notes", "plan.txt"), "plan")
	cfg = &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "work"}
	runFolderSync(t, cfg, "documents", "sync")
	return cfg, root
}

func docIDs(t *testing.T, cfg *testConfig) map[string]string {
	t.Helper()
	store, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs, err := vault.ListDocuments(context.Background(), store.DB, "work")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, d := range docs {
		m[d.Filename] = d.ID
	}
	return m
}

func decodeRemove(t *testing.T, out string) removeResult {
	t.Helper()
	var r removeResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return r
}

// Removing a discovered document drops its row, keeps the file, and the
// next scan does not add it back; restore brings it back.
func TestDocumentsRemoveDiscoveredIsNotReadded(t *testing.T) {
	cfg, root := discoveredFixture(t)
	ids := docIDs(t, cfg)
	out, err := runDocs(t, cfg, "remove", ids["career-profile.md"])
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	r := decodeRemove(t, out)
	if len(r.Removed) != 1 || !r.Removed[0].Excluded || r.Removed[0].FileDeleted || len(r.Errors) != 0 {
		t.Fatalf("remove = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "career-profile.md")); err != nil {
		t.Fatalf("file must stay on disk: %v", err)
	}
	if res := runFolderSync(t, cfg, "documents", "sync"); res.Added != 0 {
		t.Fatalf("scan re-added an excluded document: %+v", res)
	}
	if _, ok := docIDs(t, cfg)["career-profile.md"]; ok {
		t.Fatal("excluded document is listed again")
	}

	out, err = runDocs(t, cfg, "excluded")
	if err != nil {
		t.Fatal(err)
	}
	var ex []excludedListed
	if err := json.Unmarshal([]byte(out), &ex); err != nil || len(ex) != 1 || ex[0].RelativePath != "career-profile.md" {
		t.Fatalf("excluded = %s (%v)", out, err)
	}

	if out, err = runDocs(t, cfg, "restore", "career-profile.md"); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if _, ok := docIDs(t, cfg)["career-profile.md"]; !ok {
		t.Fatal("restore did not register the file again")
	}
	if _, err := runDocs(t, cfg, "restore", "career-profile.md"); err == nil {
		t.Fatal("restoring something not excluded must fail")
	}
}

func TestDocumentsRemoveByRelativePath(t *testing.T) {
	cfg, _ := discoveredFixture(t)
	out, err := runDocs(t, cfg, "remove", "notes/plan.txt")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r := decodeRemove(t, out); len(r.Removed) != 1 || !r.Removed[0].Excluded {
		t.Fatalf("remove = %+v", r)
	}
}

func TestDocumentsRemoveUploadedStillDeletesItsCopy(t *testing.T) {
	cfg, _ := discoveredFixture(t)
	src := filepath.Join(t.TempDir(), "cv.txt")
	writeFile(t, src, "cv")
	upCmd := NewCommand(testDeps(cfg))
	upCmd.SetArgs([]string{"upload-document", src})
	upCmd.SetOut(&bytes.Buffer{})
	if err := upCmd.Execute(); err != nil {
		t.Fatalf("upload: %v", err)
	}
	ids := docIDs(t, cfg)
	out, err := runDocs(t, cfg, "remove", ids["cv.txt"])
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := decodeRemove(t, out)
	if len(r.Removed) != 1 || r.Removed[0].Excluded || r.Removed[0].Source == "discovered" {
		t.Fatalf("uploaded remove = %+v", r)
	}
	if _, err := os.Stat(r.Removed[0].Path); !os.IsNotExist(err) {
		t.Fatalf("vault copy should be gone: %v", err)
	}
	if ex, _ := runDocs(t, cfg, "excluded"); strings.Contains(ex, "cv.txt") {
		t.Fatalf("an upload must not be excluded: %s", ex)
	}
}

func TestDocumentsRemoveDeleteFile(t *testing.T) {
	cfg, root := discoveredFixture(t)
	ids := docIDs(t, cfg)
	out, err := runDocs(t, cfg, "remove", "--delete-file", ids["career-profile.md"])
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r := decodeRemove(t, out); len(r.Removed) != 1 || !r.Removed[0].FileDeleted {
		t.Fatalf("remove = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "career-profile.md")); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted: %v", err)
	}
}

// --delete-file is the user's alone: an agent may remove without it.
func TestDocumentsRemoveDeleteFileNeedsTheOperator(t *testing.T) {
	cfg, root := discoveredFixture(t)
	ids := docIDs(t, cfg)
	asAgent(t)
	if _, err := runDocs(t, cfg, "remove", "--delete-file", ids["career-profile.md"]); err == nil {
		t.Fatal("an agent deleted a file from the profile folder")
	}
	if _, err := os.Stat(filepath.Join(root, "career-profile.md")); err != nil {
		t.Fatalf("file must be untouched: %v", err)
	}
	if _, ok := docIDs(t, cfg)["career-profile.md"]; !ok {
		t.Fatal("a refused remove must change nothing")
	}
	if out, err := runDocs(t, cfg, "remove", ids["career-profile.md"]); err != nil {
		t.Fatalf("agent remove without --delete-file: %v\n%s", err, out)
	}
}

func TestDocumentsRemoveUnknownReportsAndFails(t *testing.T) {
	cfg, _ := discoveredFixture(t)
	out, err := runDocs(t, cfg, "remove", "doc-999")
	if err == nil {
		t.Fatal("unknown document must fail")
	}
	if r := decodeRemove(t, out); len(r.Errors) != 1 {
		t.Fatalf("errors = %+v", r)
	}
}

func TestRemoveDiscoveredFileValidation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	writeFile(t, filepath.Join(root, "ok.md"), "x")
	writeFile(t, filepath.Join(root, "sub", "keep.md"), "x")
	writeFile(t, filepath.Join(outside, "secret.md"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "fl.md")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"outside folder":    filepath.Join(outside, "secret.md"),
		"traversal":         filepath.Join(root, "..", "outside", "secret.md"),
		"via symlinked dir": filepath.Join(root, "link", "secret.md"),
		"symlinked file":    filepath.Join(root, "fl.md"),
		"directory":         filepath.Join(root, "sub"),
		"the root itself":   root,
		"relative":          "ok.md",
		"missing":           filepath.Join(root, "nope.md"),
	}
	for name, p := range cases {
		if err := vault.RemoveDiscoveredFile(root, p); err == nil {
			t.Errorf("%s: %s was deleted", name, p)
		}
	}
	for _, p := range []string{filepath.Join(outside, "secret.md"), filepath.Join(root, "sub", "keep.md")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must survive: %v", p, err)
		}
	}
	if err := vault.RemoveDiscoveredFile(root, filepath.Join(root, "ok.md")); err != nil {
		t.Fatalf("a regular file inside the folder: %v", err)
	}
}

// asAgent makes the process look like an agent's shell.
func asAgent(t *testing.T) { t.Helper(); t.Setenv("CLAUDECODE", "1") }

// An indexed document is also forgotten by the knowledge graph; a failure
// there is a warning, never a reason to keep the document.
func TestDocumentsRemoveForgetsIndexedDocument(t *testing.T) {
	cfg, root := discoveredFixture(t)
	ids := docIDs(t, cfg)
	if out, err := runDocs(t, cfg, "index", ids["career-profile.md"]); err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	log := filepath.Join(t.TempDir(), "mcp.log")
	t.Setenv("FAKE_MCP_LOG", log)
	t.Setenv("REMOVE_FAIL", "1")
	out, err := runDocs(t, cfg, "remove", ids["career-profile.md"], ids["plan.txt"])
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := decodeRemove(t, out)
	if len(r.Removed) != 2 || len(r.Removed[0].Warnings) != 1 {
		t.Fatalf("remove = %+v", r)
	}
	raw, _ := os.ReadFile(log)
	want := "knowledge_remove\t{\"path\":\"" + filepath.Join(root, "career-profile.md") + "\"}"
	if !strings.Contains(string(raw), want) {
		t.Fatalf("knowledge_remove not called for the indexed document:\n%s", raw)
	}
	if strings.Count(string(raw), "knowledge_remove") != 1 {
		t.Fatalf("a never-indexed document must not be sent to knowledge_remove:\n%s", raw)
	}
}

// `documents rm` is the same removal as `remove`: a discovered document is excluded so the next
// folder scan does not add it back.
func TestDocumentsRmExcludesDiscoveredDocument(t *testing.T) {
	cfg, _ := discoveredFixture(t)
	ids := docIDs(t, cfg)
	if out, err := runDocs(t, cfg, "rm", ids["career-profile.md"]); err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if res := runFolderSync(t, cfg, "documents", "sync"); res.Added != 0 {
		t.Fatalf("scan re-added a document removed with rm: %+v", res)
	}
}
