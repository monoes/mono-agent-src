package profile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

func runFolderSync(t *testing.T, cfg *testConfig, args ...string) folderSyncResult {
	t.Helper()
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v (%s)", args, err, out.String())
	}
	var res folderSyncResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	return res
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedProfiles(t *testing.T, dbPath string, ids ...string) {
	t.Helper()
	store, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range ids {
		if _, err := store.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
}

func syncCounts(r folderSyncResult) [3]int { return [3]int{r.Added, r.Updated, r.Removed} }

// `profile documents sync` reconciles vault_documents with the real files
// under the profile's folder: adds, size refreshes and removals, scoped to
// the one profile, and a second run over an unchanged folder is a no-op.
func TestProfileDocumentsSync(t *testing.T) {
	setFakeMonomindOnPathCLI(t)
	dbPath := newProfileDocsCLITestDB(t)
	seedProfiles(t, dbPath, "work", "home")
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".monoagent", "profiles", "work")
	writeFile(t, filepath.Join(root, "cv.md"), "cv")
	writeFile(t, filepath.Join(root, "notes", "plan.txt"), "plan")
	writeFile(t, filepath.Join(root, "main.go"), "package main")       // not a document
	writeFile(t, filepath.Join(root, ".monomind", "x.md"), "internal") // dot-folder
	writeFile(t, filepath.Join(home, ".monoagent", "profiles", "home", "other.md"), "other profile")

	work := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "work"}
	sync := func() folderSyncResult {
		return runFolderSync(t, work, "documents", "sync")
	}

	r := sync()
	if syncCounts(r) != [3]int{2, 0, 0} || !r.Changed || r.Scanned != 2 || r.Root != root || r.ProfileID != "work" || r.Errors == nil {
		t.Fatalf("first sync = %+v", r)
	}
	if r := sync(); syncCounts(r) != [3]int{0, 0, 0} || r.Changed {
		t.Fatalf("second sync should be a no-op, got %+v", r)
	}

	writeFile(t, filepath.Join(root, "cv.md"), "a longer cv")
	if err := os.Remove(filepath.Join(root, "notes", "plan.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "letter.pdf"), "%PDF")
	if r := sync(); syncCounts(r) != [3]int{1, 1, 1} || !r.Changed || r.Scanned != 2 {
		t.Fatalf("sync after edits = %+v", r)
	}

	store, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.DB
	docs, err := vault.ListDocuments(t.Context(), db, "work")
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	for _, d := range docs {
		sizes[d.Filename] = d.SizeBytes
	}
	if len(sizes) != 2 || sizes["cv.md"] != int64(len("a longer cv")) || sizes["letter.pdf"] != 4 {
		t.Fatalf("work documents = %+v", sizes)
	}
	if other, _ := vault.ListDocuments(t.Context(), db, "home"); len(other) != 0 {
		t.Fatalf("sync of work touched profile home: %+v", other)
	}
}
