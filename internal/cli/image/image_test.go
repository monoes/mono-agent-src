package image

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// newImageCLITestDB is a migrated DB with two images in "default" (one in
// a file on disk) and one in another profile, under a temp HOME.
func newImageCLITestDB(t *testing.T) (*testConfig, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no monomind: Register's knowledge-graph sync is a no-op
	t.Cleanup(vault.Wait)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "i.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	onDisk := filepath.Join(dir, "img-002.png")
	if err := os.WriteFile(onDisk, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`
		INSERT INTO vault_images (id, seq, path, filename, size_bytes, source, workflow_id, label, profile_id, created_at)
		VALUES ('img-001', 1, '/gone/img-001.png', 'img-001.png', 10, 'gemini', 'wf-banner', NULL, 'default', '2026-09-26 10:00:00'),
		       ('img-002', 2, ?, 'img-002.png', 9, 'upload', NULL, 'Logo 100%', 'default', '2026-09-26 11:00:00'),
		       ('img-003', 3, '/x/img-003.png', 'img-003.png', 5, 'upload', NULL, 'Logo', 'work', '2026-09-26 12:00:00');
		INSERT INTO profiles (id, name, created_at) VALUES ('work', 'Work', '2026-01-01'), ('empty', 'Empty', '2026-01-01')`, onDisk); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}, dir
}

func runImage(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := NewCommand(testDeps(cfg))
	cmd.SilenceUsage = true
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func imageIDs(t *testing.T, out string) []string {
	t.Helper()
	var rows []vault.ImageEntry
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestImageListIsProfileScopedAndSnakeCase(t *testing.T) {
	cfg, _ := newImageCLITestDB(t)
	out, err := runImage(t, cfg, "list")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(imageIDs(t, out), ","); got != "img-002,img-001" {
		t.Fatalf("list = %s", got)
	}
	var rows []map[string]any
	_ = json.Unmarshal([]byte(out), &rows)
	for _, k := range []string{"id", "seq", "path", "filename", "size_bytes", "source", "workflow_id", "execution_id", "label", "created_at", "url"} {
		if _, ok := rows[1][k]; !ok {
			t.Errorf("missing %q in %v", k, rows[1])
		}
	}
	if rows[1]["url"] != "/vault-image/img-001" || rows[1]["label"] != "" || rows[1]["workflow_id"] != "wf-banner" {
		t.Errorf("row = %v", rows[1])
	}
	if out, _ := runImage(t, cfg, "list", "--limit", "1"); strings.Join(imageIDs(t, out), ",") != "img-002" {
		t.Errorf("--limit 1 = %s", out)
	}
	if _, err := runImage(t, cfg, "list", "--limit", "0"); exitCode(err) != 3 {
		t.Errorf("--limit 0: exit %d", exitCode(err))
	}

	cfg.ProfileID = "work"
	if out, _ := runImage(t, cfg, "list"); strings.Join(imageIDs(t, out), ",") != "img-003" {
		t.Errorf("work's list = %s", out)
	}
	cfg.ProfileID = "empty"
	if out, err := runImage(t, cfg, "list"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty profile list = %q, %v", out, err)
	}
}

func TestImageSearch(t *testing.T) {
	cfg, _ := newImageCLITestDB(t)
	cases := map[string]string{
		"logo":   "img-002", // label, case-insensitive; not work's img-003
		"banner": "img-001", // workflow id
		"gemini": "img-001", // source
		"100%":   "img-002", // % is literal
		"_":      "",        // _ is literal: nothing has an underscore
		"":       "img-002,img-001",
	}
	for q, want := range cases {
		out, err := runImage(t, cfg, "search", q)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if got := strings.Join(imageIDs(t, out), ","); got != want {
			t.Errorf("search %q = %q, want %q", q, got, want)
		}
	}
}

func TestImageGetStatsAndNotFound(t *testing.T) {
	cfg, _ := newImageCLITestDB(t)
	out, err := runImage(t, cfg, "get", "img-002")
	if err != nil {
		t.Fatal(err)
	}
	var im vault.ImageEntry
	if err := json.Unmarshal([]byte(out), &im); err != nil || im.ID != "img-002" || im.Label != "Logo 100%" || im.Seq != 2 {
		t.Fatalf("get = %+v, %v", im, err)
	}
	out, err = runImage(t, cfg, "stats")
	if err != nil || strings.Join(strings.Fields(out), "") != `{"count":2,"total_bytes":19}` {
		t.Fatalf("stats = %s, %v", out, err)
	}
	for _, args := range [][]string{
		{"get", "img-003"}, // another profile's
		{"data", "nope"},
		{"label", "nope", "x"},
		{"delete", "img-003"},
		{"export", "nope", filepath.Join(t.TempDir(), "x.png")},
	} {
		if _, err := runImage(t, cfg, args...); exitCode(err) != 2 {
			t.Errorf("%v: exit %d (%v)", args, exitCode(err), err)
		}
	}
}

func TestImageData(t *testing.T) {
	cfg, _ := newImageCLITestDB(t)
	out, err := runImage(t, cfg, "data", "img-002")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID, MimeType string
		SizeBytes    int    `json:"size_bytes"`
		DataURL      string `json:"data_url"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	if got.DataURL != want || got.SizeBytes != 9 || !strings.Contains(out, `"mime_type": "image/png"`) {
		t.Fatalf("data = %s", out)
	}
	// A row whose file is gone is an ordinary error, not "not found".
	if _, err := runImage(t, cfg, "data", "img-001"); err == nil || exitCode(err) == 2 {
		t.Fatalf("missing file: %v", err)
	}
}

func TestImageAddLabelExportDelete(t *testing.T) {
	cfg, dir := newImageCLITestDB(t)
	src := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(src, []byte("jpeg!"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runImage(t, cfg, "add", src, "--label", "Holiday")
	if err != nil {
		t.Fatal(err)
	}
	var im vault.ImageEntry
	if err := json.Unmarshal([]byte(out), &im); err != nil {
		t.Fatal(err)
	}
	// seq is global across profiles (work's img-003 exists), ids follow it.
	if im.ID != "img-004" || im.Seq != 4 || im.Filename != "img-004.jpg" || im.Label != "Holiday" || im.Source != "upload" || im.SizeBytes != 5 {
		t.Fatalf("add = %+v", im)
	}
	if b, err := os.ReadFile(im.Path); err != nil || string(b) != "jpeg!" || filepath.Base(im.Path) != "img-004.jpg" {
		t.Fatalf("vault copy %s: %q %v", im.Path, b, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("original must stay: %v", err)
	}

	if _, err := runImage(t, cfg, "add", filepath.Join(dir, "missing.png")); exitCode(err) != 3 {
		t.Errorf("add missing file: exit %d", exitCode(err))
	}
	if _, err := runImage(t, cfg, "add", dir); exitCode(err) != 3 {
		t.Errorf("add a directory: exit %d", exitCode(err))
	}

	if out, err := runImage(t, cfg, "label", "img-004", "Beach"); err != nil || !strings.Contains(out, `"label": "Beach"`) {
		t.Fatalf("label = %s, %v", out, err)
	}
	if _, err := runImage(t, cfg, "label", "img-004"); err != nil {
		t.Fatal(err)
	}
	out, _ = runImage(t, cfg, "get", "img-004")
	if err := json.Unmarshal([]byte(out), &im); err != nil || im.Label != "" {
		t.Fatalf("after clearing the label: %+v", im)
	}

	dest := filepath.Join(t.TempDir(), "out.jpg")
	out, err = runImage(t, cfg, "export", "img-004", dest)
	if err != nil || !strings.Contains(out, `"path": "`+dest+`"`) {
		t.Fatalf("export = %s, %v", out, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "jpeg!" {
		t.Fatalf("exported %q", b)
	}
	if _, err := runImage(t, cfg, "export", "img-004", t.TempDir()); exitCode(err) != 3 {
		t.Errorf("export to a directory: exit %d", exitCode(err))
	}

	if _, err := runImage(t, cfg, "delete", "img-004"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(im.Path); !os.IsNotExist(err) {
		t.Fatalf("vault file still there: %v", err)
	}
	if _, err := runImage(t, cfg, "get", "img-004"); exitCode(err) != 2 {
		t.Fatalf("get after delete: exit %d", exitCode(err))
	}
	// A row whose file is already gone still deletes.
	if _, err := runImage(t, cfg, "rm", "img-001"); err != nil {
		t.Fatal(err)
	}
}

// A discovered image is the user's project file tracked in place: it lists
// beside uploads, and deleting it drops only the row (ported from the
// desktop app's TestVaultImages_DiscoveredAndUploaded).
func TestImageDeleteKeepsDiscoveredFile(t *testing.T) {
	cfg, dir := newImageCLITestDB(t)
	project := filepath.Join(dir, "project", "banner.png")
	if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte("banner"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := vault.RegisterDiscoveredImage(t.Context(), db.DB, "default", project, "banner.png", 6)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	out, err := runImage(t, cfg, "list")
	if err != nil || !strings.Contains(out, `"source": "discovered"`) {
		t.Fatalf("list = %s, %v", out, err)
	}
	if _, err := runImage(t, cfg, "delete", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(project); err != nil {
		t.Fatalf("delete must keep a discovered image's file: %v", err)
	}
	if _, err := runImage(t, cfg, "get", id); exitCode(err) != 2 {
		t.Fatalf("get after delete: exit %d", exitCode(err))
	}
}

// Adding a file that is already in the profile folder records it in place
// (no copy, so `image sync` does not list it twice); deleting it keeps the
// file and keeps it out of later syncs, and adding it again brings it back.
func TestImageAddInProfileFolderDeleteAndReAdd(t *testing.T) {
	cfg, _ := newImageCLITestDB(t)
	home, _ := os.UserHomeDir()
	src := filepath.Join(home, ".monoagent", "profiles", "default", "art", "cover.png")
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func() vault.ImageEntry {
		t.Helper()
		out, err := runImage(t, cfg, "add", src)
		if err != nil {
			t.Fatal(err)
		}
		var im vault.ImageEntry
		if err := json.Unmarshal([]byte(out), &im); err != nil {
			t.Fatal(err)
		}
		return im
	}
	im := add()
	if im.Path != src || im.Filename != "cover.png" || im.Source != "upload" || im.URL != "/vault-image/"+im.ID {
		t.Fatalf("add = %+v, want the file recorded in place", im)
	}
	syncCounts := func() [3]int {
		t.Helper()
		r := runFolderSync(t, cfg, "sync")
		return [3]int{r.Added, r.Updated, r.Removed}
	}
	if c := syncCounts(); c != [3]int{0, 0, 0} {
		t.Fatalf("sync after add = %v, want no duplicate", c)
	}

	if _, err := runImage(t, cfg, "delete", im.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("delete removed the user's file: %v", err)
	}
	if c := syncCounts(); c != [3]int{0, 0, 0} {
		t.Fatalf("sync after delete = %v, want it to stay deleted", c)
	}

	if again := add(); again.Path != src {
		t.Fatalf("re-add = %+v", again)
	}
	out, _ := runImage(t, cfg, "list")
	if n := strings.Count(out, src); n != 1 {
		t.Fatalf("list has %s %d times after re-add:\n%s", src, n, out)
	}
}

// `image unignore <path>` lets sync pick a deleted discovered image up again.
func TestImageUnignore(t *testing.T) {
	cfg, _ := newImageCLITestDB(t)
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".monoagent", "profiles", "default", "logo.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("logo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runFolderSync(t, cfg, "sync"); r.Added != 1 {
		t.Fatalf("first sync = %+v", r)
	}
	out, _ := runImage(t, cfg, "list")
	var rows []vault.ImageEntry
	_ = json.Unmarshal([]byte(out), &rows)
	if _, err := runImage(t, cfg, "delete", rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if r := runFolderSync(t, cfg, "sync"); r.Added != 0 {
		t.Fatalf("sync after delete = %+v", r)
	}
	out, err := runImage(t, cfg, "unignore", path)
	if err != nil || !strings.Contains(out, `"unignored": true`) {
		t.Fatalf("unignore = %s, %v", out, err)
	}
	if r := runFolderSync(t, cfg, "sync"); r.Added != 1 {
		t.Fatalf("sync after unignore = %+v", r)
	}
	if _, err := runImage(t, cfg, "unignore", path); exitCode(err) != 2 {
		t.Errorf("unignore of a path that is not ignored: exit %d, want 2", exitCode(err))
	}
}
