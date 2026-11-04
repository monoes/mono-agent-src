package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
)

// runProfileJSON runs `profile <args> --json` against dbPath, capturing
// both stdout and the command's own writer (printJSON writes to os.Stdout).
func runProfileJSON(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	var runErr error
	var buf strings.Builder
	out := captureStdout(t, func() {
		cmd := NewCommand(testDeps(&testConfig{DBPath: dbPath, JSONOutput: true}))
		cmd.SetArgs(args)
		cmd.SetOut(&buf)
		runErr = cmd.Execute()
	})
	return out + buf.String(), runErr
}

func mustProfileJSON(t *testing.T, dbPath string, v interface{}, args ...string) {
	t.Helper()
	out, err := runProfileJSON(t, dbPath, args...)
	if err != nil {
		t.Fatalf("profile %v: %v", args, err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("profile %v printed non-JSON %q: %v", args, out, err)
	}
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	var oe *clicore.Error // operator-only refusals are the CLI's own error type
	if !errors.As(err, &oe) {
		t.Fatalf("error %v carries no exit code", err)
	}
	return oe.Code
}

func TestProfileCreateListGet(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	custom := filepath.Join(t.TempDir(), "work-folder")

	var created profileCreateResult
	mustProfileJSON(t, dbPath, &created, "create", "Work", "--root-dir", custom, "--icon", "fox")
	if created.ID == "" || created.Name != "Work" || created.RootDir != custom || created.Icon != "fox" || created.CreatedAt == "" || created.LayoutError != "" {
		t.Fatalf("create = %+v", created)
	}
	for _, dir := range []string{filepath.Join(custom, ".monoagent", "vault"), filepath.Join(custom, ".monomind")} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("create did not lay out %s: %v", dir, err)
		}
	}

	var list []map[string]interface{}
	mustProfileJSON(t, dbPath, &list, "list")
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	byID := map[string]map[string]interface{}{}
	for _, p := range list {
		for _, k := range []string{"id", "name", "created_at", "root_dir", "icon"} {
			if _, ok := p[k]; !ok {
				t.Errorf("list row %v missing %q", p, k)
			}
		}
		byID[p["id"].(string)] = p
	}
	for _, p := range list {
		if _, ok := p["active"]; ok {
			t.Errorf("list row %v still carries an active flag: there is no active profile", p)
		}
	}
	if byID[created.ID]["root_dir"] != custom || byID[created.ID]["icon"] != "fox" {
		t.Errorf("created row = %+v", byID[created.ID])
	}
	if byID["default"]["root_dir"] != profiledir.Root(nil, "default") {
		t.Errorf("default root_dir = %v, want the default location", byID["default"]["root_dir"])
	}

	var got profileJSON
	mustProfileJSON(t, dbPath, &got, "get", created.ID)
	if got.ID != created.ID || got.RootDir != custom || got.Icon != "fox" {
		t.Fatalf("get = %+v", got)
	}

	// switch and current are gone: nothing is "the" profile.
	for _, sub := range []string{"switch", "current"} {
		if _, err := runProfileJSON(t, dbPath, sub, created.ID); err == nil {
			t.Errorf("profile %s still exists", sub)
		}
	}
	_, err := runProfileJSON(t, dbPath, "get", "no-such-profile")
	if err == nil || exitCodeOf(t, err) != 2 {
		t.Fatalf("get of a missing profile: %v", err)
	}
	_, err = runProfileJSON(t, dbPath, "create", "Rel", "--root-dir", "relative/dir")
	if err == nil || exitCodeOf(t, err) != 3 {
		t.Fatalf("create with a relative folder: %v", err)
	}
	_, err = runProfileJSON(t, dbPath, "create", "  ")
	if err == nil || exitCodeOf(t, err) != 3 {
		t.Fatalf("create with a blank name: %v", err)
	}
}

// An exact id wins over another profile's name.
// The reserved id is not a reserved name: ids are generated, names are free.
func TestProfileCreateAllowsMachineAsName(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	var created profileCreateResult
	mustProfileJSON(t, dbPath, &created, "create", "_machine")
	if created.ID == "" || created.ID == "_machine" || created.Name != "_machine" {
		t.Fatalf("create = %+v", created)
	}
}

func TestProfileGetPrefersIDOverName(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	var a profileCreateResult
	mustProfileJSON(t, dbPath, &a, "create", "Alpha")
	// A profile named after Alpha's id: `create` refuses that name now, so the row is written directly.
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('b-id', ?, '2026-10-17T00:00:00Z')`, a.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var got profileJSON
	mustProfileJSON(t, dbPath, &got, "get", a.ID)
	if got.ID != a.ID {
		t.Fatalf("get %q picked %q (the profile named after it), want the id match", a.ID, got.ID)
	}
}

func TestProfileFolderEnsuresLayout(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	var got map[string]string
	mustProfileJSON(t, dbPath, &got, "folder", "default")
	want := profiledir.Root(nil, "default")
	if got["id"] != "default" || got["root_dir"] != want {
		t.Fatalf("folder = %+v, want root_dir %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(want, ".monoagent", "vault")); err != nil {
		t.Fatalf("folder did not create the layout: %v", err)
	}
}

func TestProfileMoveMovesVaultAndMonomind(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := profiledir.EnsureLayout(db.DB, "default"); err != nil {
		t.Fatal(err)
	}
	oldVault := profiledir.VaultDir(db.DB, "default")
	oldMonomind := profiledir.MonomindDir(db.DB, "default")
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	img := filepath.Join(oldVault, "img-1.png")
	doc := filepath.Join(oldVault, "documents", "doc-1.pdf")
	write(img)
	write(doc)
	write(filepath.Join(oldMonomind, "graph.db"))
	if _, err := db.DB.Exec(`INSERT INTO vault_images (id, seq, filename, path, profile_id) VALUES ('img-1', 1, 'img-1.png', ?, 'default')`, img); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO vault_documents (id, seq, filename, path, size_bytes, source, profile_id, created_at) VALUES ('doc-1', 1, 'cv.pdf', ?, 1, 'upload', 'default', '2026-09-26T10:00:00Z')`, doc); err != nil {
		t.Fatal(err)
	}
	db.Close()

	newRoot := filepath.Join(t.TempDir(), "moved")
	var checked map[string]interface{}
	mustProfileJSON(t, dbPath, &checked, "move", "default", newRoot, "--check")
	if checked["moved"] != false {
		t.Fatalf("--check = %+v", checked)
	}
	if _, err := os.Stat(img); err != nil {
		t.Fatalf("--check moved files: %v", err)
	}

	var moved map[string]interface{}
	mustProfileJSON(t, dbPath, &moved, "move", "default", newRoot)
	if moved["moved"] != true || moved["root_dir"] != newRoot || moved["moved_files"] != float64(2) {
		t.Fatalf("move = %+v", moved)
	}
	for _, p := range []string{
		filepath.Join(newRoot, ".monoagent", "vault", "img-1.png"),
		filepath.Join(newRoot, ".monoagent", "vault", "documents", "doc-1.pdf"),
		filepath.Join(newRoot, ".monomind", "graph.db"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not moved: %v", p, err)
		}
	}
	db, err = storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := profiledir.Root(db.DB, "default"); got != newRoot {
		t.Errorf("root_dir = %s, want %s", got, newRoot)
	}
	var imgPath, docPath string
	_ = db.DB.QueryRow(`SELECT path FROM vault_images WHERE id = 'img-1'`).Scan(&imgPath)
	_ = db.DB.QueryRow(`SELECT path FROM vault_documents WHERE id = 'doc-1'`).Scan(&docPath)
	if !strings.HasPrefix(imgPath, newRoot) || !strings.HasPrefix(docPath, newRoot) {
		t.Errorf("rows not repointed: image %s, document %s", imgPath, docPath)
	}

	_, err = runProfileJSON(t, dbPath, "move", "default", newRoot)
	if err == nil || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), "already this profile's folder") {
		t.Fatalf("move onto the same folder: %v", err)
	}
}

func TestProfileProjectsSuggestsUnusedMonomindProjects(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	base := t.TempDir()
	free := filepath.Join(base, "free-project")
	taken := filepath.Join(base, "taken-project")
	makeMonomindProject(t, free)
	makeMonomindProject(t, taken)
	list, _ := json.Marshal(monomindProjectsFile{Projects: []string{free, taken, filepath.Join(base, "gone")}})
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".monomind-projects.json"), list, 0o600); err != nil {
		t.Fatal(err)
	}
	var created profileCreateResult
	mustProfileJSON(t, dbPath, &created, "create", "Taken", "--root-dir", taken)

	var got []monomindProject
	mustProfileJSON(t, dbPath, &got, "projects")
	if len(got) != 1 || got[0].Path != free || got[0].Name != "free-project" {
		t.Fatalf("projects = %+v, want only %s", got, free)
	}

	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".monomind-projects.json")); err != nil {
		t.Fatal(err)
	}
	out, err := runProfileJSON(t, dbPath, "projects")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("projects without a list = %q, %v", out, err)
	}
}
