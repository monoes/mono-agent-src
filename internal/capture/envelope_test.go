package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func fixedTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func testWriter(t *testing.T) *Writer {
	t.Helper()
	return &Writer{
		Inbox: filepath.Join(t.TempDir(), "inbox"),
		Now:   func() time.Time { return fixedTime(t, "2026-09-21T10:11:12Z") },
	}
}

func TestWriteFullEnvelope(t *testing.T) {
	w := testWriter(t)
	env := &Envelope{
		Meta: Meta{
			URL:          "https://example.com/blog/post?utm_source=x",
			CanonicalURL: "https://example.com/blog/post",
			Title:        "A Post",
			CapturedAt:   "2026-09-21T10:11:12Z",
		},
		Artifacts: map[string]Artifact{
			ArtifactMHTML:      Inline([]byte("archive")),
			ArtifactPDF:        Inline([]byte("%PDF")),
			ArtifactReadable:   Inline([]byte("# A Post")),
			ArtifactScreenshot: Inline([]byte("\x89PNG")),
		},
		Warnings: []string{"lazy-load prep timed out"},
	}
	res, err := w.Write(env)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := filepath.Base(res.Path); got != "2026-09-21T10-11-12Z-example-com-blog-post" {
		t.Fatalf("directory name = %q", got)
	}
	for _, name := range []string{ArtifactMHTML, ArtifactPDF, ArtifactReadable, ArtifactScreenshot, MetaFile} {
		if _, err := os.Stat(filepath.Join(res.Path, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if len(res.Artifacts) != 4 {
		t.Fatalf("result artifacts = %v", res.Artifacts)
	}
	if res.Bytes <= 0 {
		t.Fatalf("result bytes = %d", res.Bytes)
	}
	if !sort.StringsAreSorted(res.Artifacts) {
		t.Fatalf("artifacts not sorted: %v", res.Artifacts)
	}

	blob, err := os.ReadFile(filepath.Join(res.Path, MetaFile))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(blob, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.Source != SourceExtension {
		t.Fatalf("meta.source = %q, want %q", meta.Source, SourceExtension)
	}
	// readable.md decides the dedupe hash, not the timestamp-laden MHTML.
	if !strings.HasPrefix(meta.ContentHash, "sha256:") {
		t.Fatalf("meta.contentHash = %q", meta.ContentHash)
	}
	if meta.Tags == nil {
		t.Fatal("meta.tags should serialize as [] not null")
	}
}

// TestWriteKeepsTheSelectionRange follows a selection capture all the way
// to the file a consumer actually reads. CLIP-04 exists to capture the
// range; the decode-side guard is in meta_test.go, this one makes sure
// nothing between there and meta.json flattens it on the way out.
func TestWriteKeepsTheSelectionRange(t *testing.T) {
	w := testWriter(t)
	var meta Meta
	const raw = `{"url":"https://example.com/post","selection":{"text":"the quoted sentence",
	  "truncated":false,"path":"article > p:nth-of-type(3)","startOffset":12,"endOffset":204}}`
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	res, err := w.Write(&Envelope{
		Meta:      meta,
		Artifacts: map[string]Artifact{ArtifactReadable: Inline([]byte("the quoted sentence"))},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	blob, err := os.ReadFile(filepath.Join(res.Path, MetaFile))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var onDisk struct {
		Selection map[string]any `json:"selection"`
	}
	if err := json.Unmarshal(blob, &onDisk); err != nil {
		t.Fatalf("decode meta.json: %v", err)
	}
	if onDisk.Selection == nil {
		t.Fatalf("meta.json lost the selection range:\n%s", blob)
	}
	if onDisk.Selection["path"] != "article > p:nth-of-type(3)" ||
		onDisk.Selection["startOffset"] != float64(12) ||
		onDisk.Selection["endOffset"] != float64(204) {
		t.Fatalf("meta.json selection = %v", onDisk.Selection)
	}
}

// TestWriteStreamsASpooledArtifact writes a capture whose artifact only
// ever existed as chunks on disk, and checks both that the bytes land in
// order and that the spool is consumed.
func TestWriteStreamsASpooledArtifact(t *testing.T) {
	w := testWriter(t)
	spool := filepath.Join(t.TempDir(), "spool")
	a := NewAssembler(Options{SpoolDir: spool})

	const body = "chunk-one|chunk-two|chunk-three"
	parts := []string{body[:10], body[10:20], body[20:]}
	for i, part := range parts {
		if _, err := a.Accept("c1", chunkMsg(ArtifactMHTML, i, len(parts), part)); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	env, err := a.Accept("c1", finalEnvelope(chunkedEntry(ArtifactMHTML, len(parts))))
	if err != nil {
		t.Fatalf("Accept final: %v", err)
	}
	env.Meta.URL = "https://example.com/big"

	res, err := w.Write(env)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(res.Path, ArtifactMHTML))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(got) != body {
		t.Fatalf("page.mhtml = %q, want %q", got, body)
	}
	if res.Bytes < int64(len(body)) {
		t.Fatalf("result bytes = %d, want at least %d", res.Bytes, len(body))
	}
	if countSpoolDirs(t, spool) != 0 {
		t.Fatalf("Write left the spool behind in %s", spool)
	}
	// The hash must be computed over the streamed bytes, not skipped.
	if !strings.HasPrefix(res.Meta.ContentHash, "sha256:") {
		t.Fatalf("contentHash = %q", res.Meta.ContentHash)
	}
}

// TestWriteSweepsAbandonedStagingDirs: a process killed mid-capture cannot
// clean up after itself, and what it leaves behind is not small.
func TestWriteSweepsAbandonedStagingDirs(t *testing.T) {
	w := testWriter(t)
	if err := os.MkdirAll(w.Inbox, 0o700); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	old := w.now().Add(-48 * time.Hour)
	stale := []string{tmpPrefix + "dead1", spoolPrefix + "dead2"}
	fresh := []string{tmpPrefix + "live", spoolPrefix + "live"}
	for _, name := range append(append([]string{}, stale...), fresh...) {
		dir := filepath.Join(w.Inbox, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	for _, name := range stale {
		if err := os.Chtimes(filepath.Join(w.Inbox, name), old, old); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}

	if _, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("x"))},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(w.Inbox, name)); !os.IsNotExist(err) {
			t.Errorf("stale %s survived the sweep (err=%v)", name, err)
		}
	}
	for _, name := range fresh {
		if _, err := os.Stat(filepath.Join(w.Inbox, name)); err != nil {
			t.Errorf("sweep removed a live directory %s: %v", name, err)
		}
	}
}

func TestWritePartialArtifactSet(t *testing.T) {
	w := testWriter(t)
	res, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("only this one"))},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	files, err := os.ReadDir(res.Path)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(files) != 2 { // page.mhtml + meta.json
		names := []string{}
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("directory contents = %v", names)
	}
}

func TestWriteNoArtifactsStillLandsMeta(t *testing.T) {
	w := testWriter(t)
	res, err := w.Write(&Envelope{Meta: Meta{URL: "https://example.com/"}})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Path, MetaFile)); err != nil {
		t.Fatalf("meta.json missing: %v", err)
	}
	if res.Meta.ContentHash != "" {
		t.Fatalf("hashed nothing into %q", res.Meta.ContentHash)
	}
}

func TestWriteRejectsMetaWithoutURL(t *testing.T) {
	w := testWriter(t)
	if _, err := w.Write(&Envelope{Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("x"))}}); err == nil {
		t.Fatal("wrote an envelope with no url")
	}
}

func TestWriteRejectsUnsafeArtifactNameBeforeTouchingDisk(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	w := &Writer{Inbox: inbox}
	_, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{"../../etc/passwd": Inline([]byte("nope"))},
	})
	if err == nil {
		t.Fatal("wrote an artifact with a traversing name")
	}
	if _, statErr := os.Stat(inbox); statErr == nil {
		t.Fatal("a rejected envelope should not create the inbox")
	}
}

// TestWriteLeavesNoStagingDirectory is the half-written-capture guarantee:
// after a successful write there is exactly one published directory and no
// leftover staging directory for a watcher to trip over.
func TestWriteLeavesNoStagingDirectory(t *testing.T) {
	w := testWriter(t)
	res, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("x"))},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(w.Inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 || filepath.Join(w.Inbox, entries[0].Name()) != res.Path {
		t.Fatalf("inbox entries = %v, want just the published capture", entries)
	}
	if strings.HasPrefix(entries[0].Name(), ".") {
		t.Fatal("published directory is still dot-prefixed (never renamed)")
	}
}

// TestWriteFailureLeavesInboxClean covers the same guarantee on the error
// path: a capture that cannot be written must publish nothing at all.
func TestWriteFailureLeavesInboxClean(t *testing.T) {
	w := testWriter(t)
	// Pre-create the inbox and make it read-only so the staging write
	// fails midway.
	if err := os.MkdirAll(w.Inbox, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(w.Inbox, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.Inbox, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory is still writable")
	}
	if _, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("x"))},
	}); err == nil {
		t.Fatal("expected a write failure")
	}
	entries, err := os.ReadDir(w.Inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("inbox entries after a failed write = %v", entries)
	}
}

func TestWriteCollidingCapturesGetDistinctDirectories(t *testing.T) {
	w := testWriter(t)
	env := func() *Envelope {
		return &Envelope{
			Meta:      Meta{URL: "https://example.com/same", CapturedAt: "2026-09-21T10:11:12Z"},
			Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("x"))},
		}
	}
	first, err := w.Write(env())
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := w.Write(env())
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if first.Path == second.Path {
		t.Fatalf("both captures landed in %s", first.Path)
	}
	if !strings.HasSuffix(second.Path, "-2") {
		t.Fatalf("second path = %s, want a -2 suffix", second.Path)
	}
}

func TestWriteArtifactPermissionsAreOwnerOnly(t *testing.T) {
	w := testWriter(t)
	res, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("logged-in page contents"))},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(filepath.Join(res.Path, ArtifactMHTML))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 { // Windows has no Unix modes
		t.Fatalf("artifact mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o700 {
		t.Fatalf("directory mode = %o, want 700", perm)
	}
}

func TestWriteUsesNowWhenCapturedAtMissing(t *testing.T) {
	w := testWriter(t)
	res, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/"},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("x"))},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Meta.CapturedAt != "2026-09-21T10:11:12Z" {
		t.Fatalf("capturedAt = %q", res.Meta.CapturedAt)
	}
	if !strings.HasPrefix(filepath.Base(res.Path), "2026-09-21T10-11-12Z-") {
		t.Fatalf("directory = %q", filepath.Base(res.Path))
	}
}

func TestWriteKeepsSenderContentHash(t *testing.T) {
	w := testWriter(t)
	res, err := w.Write(&Envelope{
		Meta:      Meta{URL: "https://example.com/", ContentHash: "sha256:deadbeef"},
		Artifacts: map[string]Artifact{ArtifactReadable: Inline([]byte("body"))},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Meta.ContentHash != "sha256:deadbeef" {
		t.Fatalf("contentHash = %q, want the sender's", res.Meta.ContentHash)
	}
}

func TestContentHashPrefersReadable(t *testing.T) {
	artifacts := map[string]Artifact{
		ArtifactMHTML:    Inline([]byte("archive with request ids")),
		ArtifactReadable: Inline([]byte("body")),
	}
	names, err := sortedArtifactNames(artifacts)
	if err != nil {
		t.Fatalf("sortedArtifactNames: %v", err)
	}
	readableOnly, err := contentHash(map[string]Artifact{ArtifactReadable: Inline([]byte("body"))}, []string{ArtifactReadable})
	if err != nil {
		t.Fatalf("contentHash: %v", err)
	}
	got, err := contentHash(artifacts, names)
	if err != nil {
		t.Fatalf("contentHash: %v", err)
	}
	if got != readableOnly {
		t.Fatalf("hash = %q, want the readable-only hash %q", got, readableOnly)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"https://www.example.com/blog/post":  "example-com-blog-post",
		"https://example.com/":               "example-com",
		"https://example.com":                "example-com",
		"https://example.com/a?b=c#d":        "example-com-a",
		"https://EXAMPLE.com/Path/To/Thing":  "example-com-path-to-thing",
		"not a url at all":                   "not-a-url-at-all",
		"":                                   "capture",
		"https://example.com/../../etc/pass": "example-com-etc-pass",
		"file:///home/me/notes.html":         "file-home-me-notes-html",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugIsBounded(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("segment/", 40)
	got := Slug(long)
	if len(got) > maxSlugLen {
		t.Fatalf("slug is %d chars: %q", len(got), got)
	}
	if strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
		t.Fatalf("slug has dangling dashes: %q", got)
	}
}

func TestValidArtifactName(t *testing.T) {
	valid := []string{ArtifactMHTML, ArtifactPDF, ArtifactReadable, ArtifactScreenshot, "table-1.csv", "transcript_en.md"}
	for _, name := range valid {
		if !ValidArtifactName(name) {
			t.Errorf("ValidArtifactName(%q) = false, want true", name)
		}
	}
	invalid := []string{
		"", ".", "..", "../x", "a/b", "a\\b", "meta.json", ".hidden", "trailing.",
		"nul.txt", "CON", "page.mhtml\x00", strings.Repeat("a", 65), "page mhtml",
		"a..b",
	}
	for _, name := range invalid {
		if ValidArtifactName(name) {
			t.Errorf("ValidArtifactName(%q) = true, want false", name)
		}
	}
}
