package captureexport

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
)

// R11-11: an overwrite publishes the new capture before the old one is
// deleted, so there is no instant with neither.
func TestOverwriteKeepsTheOriginalUntilTheNewOneIsPublished(t *testing.T) {
	src := seed(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	dst := filepath.Join(t.TempDir(), "inbox")
	roundTrip(t, src, ImportOptions{Inbox: dst})
	entries, _ := capture.List(dst)
	local := filepath.Join(entries[0].Path, "local-only.txt")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sawOld, sawNew bool
	beforeOldRemoved = func(old string) {
		if _, err := os.Stat(filepath.Join(old, "local-only.txt")); err == nil {
			sawOld = true
		}
		if _, err := os.Stat(filepath.Join(entries[0].Path, capture.MetaFile)); err == nil {
			sawNew = true
		}
	}
	t.Cleanup(func() { beforeOldRemoved = func(string) {} })
	roundTrip(t, src, ImportOptions{Inbox: dst, OnCollision: CollisionOverwrite})
	if !sawOld || !sawNew {
		t.Fatalf("old kept aside = %v, new published = %v", sawOld, sawNew)
	}
	left, _ := filepath.Glob(filepath.Join(dst, ".tmp-old-*"))
	if len(left) != 0 {
		t.Errorf("left over: %v", left)
	}
}

// R11-12: Profile and Source in an imported meta.json are not the archive's to choose.
func TestImportRewritesProfileAndForgedSource(t *testing.T) {
	man, _ := json.Marshal(Manifest{Format: Format, Version: Version})
	blob := buildArchive(t, func(tw *tar.Writer) {
		for _, e := range []struct{ name, body string }{
			{ManifestPath, string(man)},
			{"captures/2026-09-20T10-00-00Z-a/meta.json", `{"url":"https://x.test/","profile":"someone-else","source":"made-up"}`},
			{"captures/2026-09-20T10-00-01Z-b/meta.json", `{"url":"https://x.test/b","source":"recording"}`},
		} {
			if err := writeFileEntry(tw, e.name, []byte(e.body), zeroTime()); err != nil {
				t.Fatal(err)
			}
		}
	})
	dst := filepath.Join(t.TempDir(), "inbox")
	if _, err := Import(bytes.NewReader(blob), ImportOptions{Inbox: dst, Profile: "work"}); err != nil {
		t.Fatal(err)
	}
	a, err := capture.ReadMeta(filepath.Join(dst, "2026-09-20T10-00-00Z-a"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Profile != "work" || a.Source != capture.SourceExtension {
		t.Errorf("profile %q source %q", a.Profile, a.Source)
	}
	b, _ := capture.ReadMeta(filepath.Join(dst, "2026-09-20T10-00-01Z-b"))
	if b.Source != capture.SourceRecording || strings.TrimSpace(b.Profile) != "work" {
		t.Errorf("recording kept as %q / %q", b.Source, b.Profile)
	}
}
