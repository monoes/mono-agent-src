package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/vault"
)

// A node that writes an image into the profile folder (data.write_binary_file
// there) gets it registered in place, so `image sync` does not add a second
// row for the same file.
func TestAutoRegisterItemImages_ProfileFolderFileIsNotDuplicated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	_, db := newMigratedStore(t)
	path := filepath.Join(profiledir.Root(db, "default"), "out", "chart.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
	outputs := []NodeOutput{{Handle: "main", Items: []Item{{JSON: map[string]interface{}{"file_path": path}}}}}
	autoRegisterItemImages(ctx, outputs, "wf-1", "ex-1")

	id, _ := outputs[0].Items[0].JSON["vault_id"].(string)
	im, err := vault.GetImage(ctx, db, "default", id)
	if err != nil || im.Path != path || im.Source != "workflow" || im.WorkflowID != "wf-1" {
		t.Fatalf("registered image = %+v, %v; want a workflow row for %s", im, err, path)
	}
	r := vault.SyncDiscoveredImages(ctx, db, "default", []vault.DiscoveredFile{{Path: path, Filename: "chart.png", SizeBytes: 3}})
	if r.Added != 0 || len(r.Errs) != 0 {
		t.Fatalf("sync = %+v, want the file already tracked", r)
	}
}

// A node that writes audio or video registers it too, with its kind, and a
// file that is no media (a text file) is left alone.
func TestAutoRegisterItemImages_AudioAndVideo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	_, db := newMigratedStore(t)
	dir := t.TempDir()
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
	for name, kind := range map[string]string{"take.mp3": "audio", "clip.mp4": "video", "notes.txt": ""} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		outputs := []NodeOutput{{Handle: "main", Items: []Item{{JSON: map[string]interface{}{"file_path": p}}}}}
		autoRegisterItemImages(ctx, outputs, "wf-1", "ex-1")
		id, _ := outputs[0].Items[0].JSON["vault_id"].(string)
		if kind == "" {
			if id != "" {
				t.Errorf("%s was registered as %s, want it left out", name, id)
			}
			continue
		}
		im, err := vault.GetImage(ctx, db, "default", id)
		if err != nil || im.Kind != kind {
			t.Errorf("%s: entry = %+v, %v; want kind %q", name, im, err, kind)
		}
	}
}
