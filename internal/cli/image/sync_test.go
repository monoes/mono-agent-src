package image

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// writePNG writes a real w×h PNG to path and returns its size.
func writePNG(t *testing.T, path string, w, h int) int64 {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{R: uint8(x), G: 90, B: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, buf.String())
	return int64(buf.Len())
}

// `image sync` reconciles the discovered images with the real image files
// under the profile's folder: adds, size refreshes and removals, scoped to
// the one profile, leaving images from other sources alone, and a second
// run over an unchanged folder is a no-op.
func TestImageSync(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	seedProfiles(t, dbPath, "work", "home")
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".monoagent", "profiles", "work")
	writePNG(t, filepath.Join(root, "logo.png"), 4, 4)
	writePNG(t, filepath.Join(root, "assets", "banner.png"), 8, 2)
	writeFile(t, filepath.Join(root, "notes.md"), "not an image")
	writePNG(t, filepath.Join(root, ".monoagent", "vault", "img-900.png"), 2, 2) // upload copies: dot-folder
	writePNG(t, filepath.Join(root, "node_modules", "pkg", "icon.png"), 2, 2)
	chat := filepath.Join(root, "images", "chat.png")
	writePNG(t, chat, 3, 3)
	writePNG(t, filepath.Join(home, ".monoagent", "profiles", "home", "other.png"), 2, 2)

	store, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := store.DB
	// An image the chat saved into the folder keeps its own row (its stale
	// size is refreshed: any row on a scanned path tracks that file).
	if _, err := db.Exec(`INSERT INTO vault_images (id, seq, path, filename, size_bytes, source, profile_id, created_at)
		VALUES ('img-001', 1, ?, 'chat.png', 1, 'chat', 'work', '2026-09-26 10:00:00')`, chat); err != nil {
		t.Fatal(err)
	}

	work := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "work"}
	sync := func() folderSyncResult { return runFolderSync(t, work, "sync") }

	r := sync()
	if syncCounts(r) != [3]int{2, 1, 0} || !r.Changed || r.Scanned != 3 || r.Root != root || r.ProfileID != "work" || r.Errors == nil {
		t.Fatalf("first sync = %+v", r)
	}
	if r := sync(); syncCounts(r) != [3]int{0, 0, 0} || r.Changed || len(r.Errors) != 0 {
		t.Fatalf("second sync should be a no-op, got %+v", r)
	}

	logoSize := writePNG(t, filepath.Join(root, "logo.png"), 32, 32)
	if err := os.Remove(filepath.Join(root, "assets", "banner.png")); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(root, "photos", "trip.png"), 5, 5)
	if r := sync(); syncCounts(r) != [3]int{1, 1, 1} || !r.Changed || r.Scanned != 3 {
		t.Fatalf("sync after edits = %+v", r)
	}
	if r := sync(); r.Changed {
		t.Fatalf("sync after the edits settled = %+v", r)
	}

	images, err := vault.ListImages(t.Context(), db, "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]vault.ImageEntry{}
	for _, im := range images {
		got[im.Filename] = im
	}
	if len(got) != 3 || got["logo.png"].SizeBytes != logoSize || got["logo.png"].Source != "discovered" ||
		got["trip.png"].Source != "discovered" || got["chat.png"].Source != "chat" || got["chat.png"].ID != "img-001" {
		t.Fatalf("work images = %+v", got)
	}
	if other, _ := vault.ListImages(t.Context(), db, "home", 100); len(other) != 0 {
		t.Fatalf("sync of work touched profile home: %+v", other)
	}

	home2 := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "home"}
	if r := runFolderSync(t, home2, "sync"); syncCounts(r) != [3]int{1, 0, 0} {
		t.Fatalf("home sync = %+v", r)
	}
	if r := sync(); r.Changed {
		t.Fatalf("home's sync changed work: %+v", r)
	}
}

// An unknown or malformed profile is invalid input (exit 3), and nothing
// is scanned.
func TestImageSyncInvalidProfile(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	for _, p := range []string{"nope", "../escape"} {
		cfg := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: p}
		if _, err := runImage(t, cfg, "sync"); exitCode(err) != 3 {
			t.Errorf("profile %q: exit %d (%v)", p, exitCode(err), err)
		}
	}
}
