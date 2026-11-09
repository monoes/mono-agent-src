package vault

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/profiledir"
)

func TestMoveFiles_MovesAndUpdatesPaths(t *testing.T) {
	db := newVaultTestDB(t)
	fromDir := t.TempDir()
	toDir := filepath.Join(t.TempDir(), "dest") // doesn't exist yet — MoveFiles must create it

	srcPath := filepath.Join(fromDir, "img-001.png")
	content := []byte("fake image bytes")
	if err := os.WriteFile(srcPath, content, 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-001', ?, 'img-001.png', 'p1')`, srcPath); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	moved, errs := MoveFiles(context.Background(), db, "p1", fromDir, toDir)
	if len(errs) != 0 {
		t.Fatalf("MoveFiles errors: %v", errs)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}

	destPath := filepath.Join(toDir, "img-001.png")
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("source file still exists at %s", srcPath)
	}
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading moved file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("moved file content mismatch: got %q want %q", got, content)
	}

	var dbPath string
	if err := db.QueryRow(`SELECT path FROM vault_images WHERE id = 'img-001'`).Scan(&dbPath); err != nil {
		t.Fatalf("reading updated path: %v", err)
	}
	if dbPath != destPath {
		t.Fatalf("db path = %q, want %q", dbPath, destPath)
	}
}

func TestMoveFiles_SecondCallIsNoOp(t *testing.T) {
	db := newVaultTestDB(t)
	fromDir := t.TempDir()
	toDir := t.TempDir()

	srcPath := filepath.Join(fromDir, "img-001.png")
	if err := os.WriteFile(srcPath, []byte("x"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-001', ?, 'img-001.png', 'p1')`, srcPath); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if moved, errs := MoveFiles(context.Background(), db, "p1", fromDir, toDir); moved != 1 || len(errs) != 0 {
		t.Fatalf("first move: moved=%d errs=%v", moved, errs)
	}

	// Re-run with the SAME fromDir — the row's path is now under toDir, not
	// fromDir, so this must be a clean no-op rather than an error.
	moved, errs := MoveFiles(context.Background(), db, "p1", fromDir, toDir)
	if len(errs) != 0 {
		t.Fatalf("second move errors: %v", errs)
	}
	if moved != 0 {
		t.Fatalf("second move moved = %d, want 0 (already moved)", moved)
	}
}

func TestMoveFiles_OnlyMovesMatchingProfile(t *testing.T) {
	db := newVaultTestDB(t)
	fromDir := t.TempDir()
	toDir := t.TempDir()

	pathA := filepath.Join(fromDir, "img-a.png")
	pathB := filepath.Join(fromDir, "img-b.png")
	os.WriteFile(pathA, []byte("a"), 0600)
	os.WriteFile(pathB, []byte("b"), 0600)
	db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-a', ?, 'img-a.png', 'p1')`, pathA)
	db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-b', ?, 'img-b.png', 'p2')`, pathB)

	moved, errs := MoveFiles(context.Background(), db, "p1", fromDir, toDir)
	if len(errs) != 0 || moved != 1 {
		t.Fatalf("moved=%d errs=%v, want moved=1 no errs", moved, errs)
	}
	if _, err := os.Stat(pathB); err != nil {
		t.Fatalf("profile p2's file was moved too: %v", err)
	}
}

// setHomeForTest points profiledir's default-root resolution at a fresh temp
// dir, mirroring internal/profiledir/profiledir_test.go's own pattern —
// MigrateVaultDirIntoMonoagent resolves paths via profiledir.Root/VaultDir,
// which fall back to $HOME/.monoagent/profiles/<id>/ when db has no
// profiles.root_dir override (newVaultTestDB's DB has no profiles table at
// all, so Root's override lookup errors and falls through to the default —
// exactly the case this exercises).
func setHomeForTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)
	return home
}

func TestMigrateVaultDirIntoMonoagent_MovesFromOldPerProfilePathAndRemovesEmptyDir(t *testing.T) {
	setHomeForTest(t)
	db := newVaultTestDB(t)

	root := profiledir.Root(db, "p1")
	oldDir := filepath.Join(root, "vault")
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatalf("seed old vault dir: %v", err)
	}
	srcPath := filepath.Join(oldDir, "img-001.png")
	if err := os.WriteFile(srcPath, []byte("fake image bytes"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-001', ?, 'img-001.png', 'p1')`, srcPath); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "p1")
	if len(errs) != 0 {
		t.Fatalf("MigrateVaultDirIntoMonoagent errors: %v", errs)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}

	newPath := filepath.Join(profiledir.VaultDir(db, "p1"), "img-001.png")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("file not at new .monoagent/vault path: %v", err)
	}
	var dbPath string
	if err := db.QueryRow(`SELECT path FROM vault_images WHERE id = 'img-001'`).Scan(&dbPath); err != nil {
		t.Fatalf("reading updated path: %v", err)
	}
	if dbPath != newPath {
		t.Fatalf("db path = %q, want %q", dbPath, newPath)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old vault/ dir should have been removed once empty, stat err = %v", err)
	}
}

func TestMigrateVaultDirIntoMonoagent_SecondCallIsNoOp(t *testing.T) {
	setHomeForTest(t)
	db := newVaultTestDB(t)

	root := profiledir.Root(db, "p1")
	oldDir := filepath.Join(root, "vault")
	os.MkdirAll(oldDir, 0700)
	srcPath := filepath.Join(oldDir, "img-001.png")
	os.WriteFile(srcPath, []byte("x"), 0600)
	db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-001', ?, 'img-001.png', 'p1')`, srcPath)

	if moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "p1"); moved != 1 || len(errs) != 0 {
		t.Fatalf("first call: moved=%d errs=%v", moved, errs)
	}

	moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "p1")
	if len(errs) != 0 {
		t.Fatalf("second call errors: %v", errs)
	}
	if moved != 0 {
		t.Fatalf("second call moved = %d, want 0 (already migrated)", moved)
	}
}

func TestMigrateVaultDirIntoMonoagent_LeavesNonEmptyOldDirIfUnknownFileRemains(t *testing.T) {
	setHomeForTest(t)
	db := newVaultTestDB(t)

	root := profiledir.Root(db, "p1")
	oldDir := filepath.Join(root, "vault")
	os.MkdirAll(oldDir, 0700)
	trackedPath := filepath.Join(oldDir, "img-001.png")
	os.WriteFile(trackedPath, []byte("tracked"), 0600)
	db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-001', ?, 'img-001.png', 'p1')`, trackedPath)
	// A file the DB never knew about (e.g. left over by hand) — must never
	// be silently discarded.
	strayPath := filepath.Join(oldDir, "stray-notes.txt")
	os.WriteFile(strayPath, []byte("do not delete me"), 0600)

	moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "p1")
	if len(errs) != 0 || moved != 1 {
		t.Fatalf("moved=%d errs=%v, want moved=1 no errs", moved, errs)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("old vault/ dir should still exist (non-empty): %v", err)
	}
	if _, err := os.Stat(strayPath); err != nil {
		t.Fatalf("stray untracked file must be left in place: %v", err)
	}
}

func TestMigrateVaultDirIntoMonoagent_NoOldDirIsCleanNoOp(t *testing.T) {
	setHomeForTest(t)
	db := newVaultTestDB(t)

	moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "fresh-profile")
	if len(errs) != 0 || moved != 0 {
		t.Fatalf("moved=%d errs=%v, want moved=0 no errs for a profile with no legacy vault dir", moved, errs)
	}
}

func TestMoveDocumentFiles_MovesAndUpdatesPaths(t *testing.T) {
	db := newVaultTestDB(t)
	fromDir := t.TempDir()
	toDir := filepath.Join(t.TempDir(), "dest")

	srcPath := filepath.Join(fromDir, "doc-001.pdf")
	content := []byte("fake pdf bytes")
	if err := os.WriteFile(srcPath, content, 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_documents (id, path, filename, profile_id, source) VALUES ('doc-001', ?, 'doc-001.pdf', 'p1', 'upload')`, srcPath); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	moved, errs := MoveDocumentFiles(context.Background(), db, "p1", fromDir, toDir)
	if len(errs) != 0 {
		t.Fatalf("MoveDocumentFiles errors: %v", errs)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}

	destPath := filepath.Join(toDir, "doc-001.pdf")
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("source file still exists at %s", srcPath)
	}
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading moved file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("moved file content mismatch: got %q want %q", got, content)
	}

	var dbPath string
	if err := db.QueryRow(`SELECT path FROM vault_documents WHERE id = 'doc-001'`).Scan(&dbPath); err != nil {
		t.Fatalf("reading updated path: %v", err)
	}
	if dbPath != destPath {
		t.Fatalf("db path = %q, want %q", dbPath, destPath)
	}
}

// TestMoveDocumentFiles_UsesPathBasenameAvoidingFilenameCollisions is the
// regression test for a real hazard specific to vault_documents:
// RegisterDocument stores the ORIGINAL uploaded filename in the `filename`
// column (e.g. "resume.pdf") while the actual on-disk/DB path uses a
// unique id-based name (e.g. "doc-001.pdf") -- see RegisterDocument's
// destPath construction. Two different uploads named identically by the
// user ("resume.pdf") therefore have distinct paths but an IDENTICAL
// filename column. Building the destination name from the filename column
// (as MoveFiles' shared engine originally did) would make both rows
// resolve to the same destPath, and the second os.Rename would silently
// clobber the first with no reported error -- a real, silent data-loss
// hazard. Building it from the CURRENT path's own basename instead (which
// is already unique, since it's what's on disk right now) avoids the
// collision entirely, for both tables uniformly.
func TestMoveDocumentFiles_UsesPathBasenameAvoidingFilenameCollisions(t *testing.T) {
	db := newVaultTestDB(t)
	fromDir := t.TempDir()
	toDir := t.TempDir()

	path1 := filepath.Join(fromDir, "doc-001.pdf")
	path2 := filepath.Join(fromDir, "doc-002.pdf")
	os.WriteFile(path1, []byte("first upload"), 0600)
	os.WriteFile(path2, []byte("second upload"), 0600)
	// Both rows share the SAME original filename -- the collision-prone case.
	db.Exec(`INSERT INTO vault_documents (id, path, filename, profile_id, source) VALUES ('doc-001', ?, 'resume.pdf', 'p1', 'upload')`, path1)
	db.Exec(`INSERT INTO vault_documents (id, path, filename, profile_id, source) VALUES ('doc-002', ?, 'resume.pdf', 'p1', 'upload')`, path2)

	moved, errs := MoveDocumentFiles(context.Background(), db, "p1", fromDir, toDir)
	if len(errs) != 0 {
		t.Fatalf("MoveDocumentFiles errors: %v", errs)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2", moved)
	}

	got1, err := os.ReadFile(filepath.Join(toDir, "doc-001.pdf"))
	if err != nil {
		t.Fatalf("doc-001.pdf missing after move: %v", err)
	}
	if string(got1) != "first upload" {
		t.Fatalf("doc-001.pdf content = %q, want %q (collision clobbered it)", got1, "first upload")
	}
	got2, err := os.ReadFile(filepath.Join(toDir, "doc-002.pdf"))
	if err != nil {
		t.Fatalf("doc-002.pdf missing after move: %v", err)
	}
	if string(got2) != "second upload" {
		t.Fatalf("doc-002.pdf content = %q, want %q", got2, "second upload")
	}

	var dbPath1, dbPath2 string
	db.QueryRow(`SELECT path FROM vault_documents WHERE id = 'doc-001'`).Scan(&dbPath1)
	db.QueryRow(`SELECT path FROM vault_documents WHERE id = 'doc-002'`).Scan(&dbPath2)
	if dbPath1 == dbPath2 {
		t.Fatalf("both rows resolved to the same path %q -- collision", dbPath1)
	}
}

// TestMigrateVaultDirIntoMonoagent_MovesDocumentsToo is the regression test
// for a gap the images-only version of this migration left: vault_documents
// (uploaded documents, stored under the same old <root>/vault/documents/ as
// images live in <root>/vault/) was never touched, so the old vault/ dir
// would never actually go away for a profile with any uploaded documents,
// and new documents would split across two locations (old for existing
// rows, new .monoagent/vault/documents/ for anything registered after this
// change). Both the image and the document must move, and the now-fully-
// empty old vault/ dir (including its now-empty documents/ subdirectory)
// must be removed.
func TestMigrateVaultDirIntoMonoagent_MovesDocumentsToo(t *testing.T) {
	setHomeForTest(t)
	db := newVaultTestDB(t)

	root := profiledir.Root(db, "p1")
	oldDir := filepath.Join(root, "vault")
	oldDocsDir := filepath.Join(oldDir, "documents")
	if err := os.MkdirAll(oldDocsDir, 0700); err != nil {
		t.Fatalf("seed old documents dir: %v", err)
	}

	imgPath := filepath.Join(oldDir, "img-001.png")
	os.WriteFile(imgPath, []byte("img"), 0600)
	db.Exec(`INSERT INTO vault_images (id, path, filename, profile_id) VALUES ('img-001', ?, 'img-001.png', 'p1')`, imgPath)

	docPath := filepath.Join(oldDocsDir, "doc-001.pdf")
	os.WriteFile(docPath, []byte("doc"), 0600)
	db.Exec(`INSERT INTO vault_documents (id, path, filename, profile_id, source) VALUES ('doc-001', ?, 'doc-001.pdf', 'p1', 'upload')`, docPath)

	moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "p1")
	if len(errs) != 0 {
		t.Fatalf("MigrateVaultDirIntoMonoagent errors: %v", errs)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2 (1 image + 1 document)", moved)
	}

	newDocPath := filepath.Join(profiledir.VaultDir(db, "p1"), "documents", "doc-001.pdf")
	if _, err := os.Stat(newDocPath); err != nil {
		t.Fatalf("document not at new .monoagent/vault/documents path: %v", err)
	}
	var dbPath string
	if err := db.QueryRow(`SELECT path FROM vault_documents WHERE id = 'doc-001'`).Scan(&dbPath); err != nil {
		t.Fatalf("reading updated document path: %v", err)
	}
	if dbPath != newDocPath {
		t.Fatalf("document db path = %q, want %q", dbPath, newDocPath)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old vault/ dir should have been fully removed once empty (including documents/), stat err = %v", err)
	}
}

// TestMigrateVaultDirIntoMonoagent_LeavesDiscoveredDocumentOutsideVaultUntouched
// is the safety property the fromDir prefix check exists for: a discovered
// document's path lives wherever the user's own project put it (e.g. under
// the docs/ TaxonomyFolder), never inside the vault directory, and this
// migration must never move or rewrite it -- RegisterDiscoveredDocument
// never copied that file into monoagent's ownership, and moving it would
// silently sever the row from the user's real file.
func TestMigrateVaultDirIntoMonoagent_LeavesDiscoveredDocumentOutsideVaultUntouched(t *testing.T) {
	setHomeForTest(t)
	db := newVaultTestDB(t)

	root := profiledir.Root(db, "p1")
	docsFolder := filepath.Join(root, "docs")
	if err := os.MkdirAll(docsFolder, 0700); err != nil {
		t.Fatalf("seed docs folder: %v", err)
	}
	discoveredPath := filepath.Join(docsFolder, "notes.md")
	if err := os.WriteFile(discoveredPath, []byte("user's own notes"), 0600); err != nil {
		t.Fatalf("seed discovered file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_documents (id, path, filename, profile_id, source) VALUES ('doc-002', ?, 'notes.md', 'p1', 'discovered')`, discoveredPath); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	moved, errs := MigrateVaultDirIntoMonoagent(context.Background(), db, "p1")
	if len(errs) != 0 || moved != 0 {
		t.Fatalf("moved=%d errs=%v, want moved=0 no errs (nothing under the old vault dir)", moved, errs)
	}

	if _, err := os.Stat(discoveredPath); err != nil {
		t.Fatalf("discovered file must be left in place: %v", err)
	}
	var dbPath string
	if err := db.QueryRow(`SELECT path FROM vault_documents WHERE id = 'doc-002'`).Scan(&dbPath); err != nil {
		t.Fatalf("reading path: %v", err)
	}
	if dbPath != discoveredPath {
		t.Fatalf("discovered document's db path must be untouched: got %q, want %q", dbPath, discoveredPath)
	}
}
