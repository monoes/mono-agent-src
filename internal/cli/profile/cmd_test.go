package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if !errors.As(err, &ce) {
		t.Fatalf("error %v carries no exit code", err)
	}
	return ce.code
}

func TestProfileCreateListGetSwitch(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	custom := filepath.Join(t.TempDir(), "work-folder")

	var created profileCreateResult
	mustProfileJSON(t, dbPath, &created, "create", "Work", "--root-dir", custom, "--icon", "fox")
	if created.ID == "" || created.Name != "Work" || created.RootDir != custom || created.Icon != "fox" || created.Active || created.CreatedAt == "" || created.LayoutError != "" {
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
		for _, k := range []string{"id", "name", "created_at", "root_dir", "icon", "active"} {
			if _, ok := p[k]; !ok {
				t.Errorf("list row %v missing %q", p, k)
			}
		}
		byID[p["id"].(string)] = p
	}
	if byID["default"]["active"] != true || byID[created.ID]["active"] != false {
		t.Errorf("active flags = %+v", list)
	}
	if byID[created.ID]["root_dir"] != custom || byID[created.ID]["icon"] != "fox" {
		t.Errorf("created row = %+v", byID[created.ID])
	}
	if byID["default"]["root_dir"] != profiledir.Root(nil, "default") {
		t.Errorf("default root_dir = %v, want the default location", byID["default"]["root_dir"])
	}

	var switched map[string]string
	mustProfileJSON(t, dbPath, &switched, "switch", created.ID)
	if switched["id"] != created.ID {
		t.Fatalf("switch = %+v", switched)
	}
	var got profileJSON
	mustProfileJSON(t, dbPath, &got, "get", created.ID)
	if got.ID != created.ID || !got.Active || got.RootDir != custom || got.Icon != "fox" {
		t.Fatalf("get = %+v", got)
	}

	_, err := runProfileJSON(t, dbPath, "switch", "no-such-profile")
	if err == nil || exitCodeOf(t, err) != 2 {
		t.Fatalf("switch to a missing profile: %v", err)
	}
	_, err = runProfileJSON(t, dbPath, "get", "no-such-profile")
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
func TestProfileSwitchPrefersIDOverName(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	var a profileCreateResult
	mustProfileJSON(t, dbPath, &a, "create", "Alpha")
	var b profileCreateResult
	mustProfileJSON(t, dbPath, &b, "create", a.ID) // named after Alpha's id

	var switched map[string]string
	mustProfileJSON(t, dbPath, &switched, "switch", a.ID)
	if switched["id"] != a.ID {
		t.Fatalf("switch %q picked %q (the profile named after it), want the id match", a.ID, switched["id"])
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
