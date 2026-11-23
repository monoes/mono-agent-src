package capture

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seed writes one envelope into inbox at the given capture time.
func seed(t *testing.T, inbox, url, title, at string) string {
	t.Helper()
	w := &Writer{Inbox: inbox, Now: func() time.Time { return time.Now() }}
	res, err := w.Write(&Envelope{
		Meta:      Meta{URL: url, Title: title, CapturedAt: at},
		Artifacts: map[string]Artifact{ArtifactMHTML: Inline([]byte("archive")), ArtifactReadable: Inline([]byte("# t"))},
	})
	if err != nil {
		t.Fatalf("seed %s: %v", url, err)
	}
	return res.Path
}

func TestListReturnsNewestFirst(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	seed(t, inbox, "https://example.com/old", "Old", "2026-09-19T08:00:00Z")
	newest := seed(t, inbox, "https://example.com/new", "New", "2026-09-21T08:00:00Z")
	seed(t, inbox, "https://example.com/mid", "Mid", "2026-09-20T08:00:00Z")

	entries, err := List(inbox)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Path != newest {
		t.Fatalf("first entry = %s, want %s", entries[0].Path, newest)
	}
	if entries[0].Title != "New" || entries[0].URL != "https://example.com/new" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].Bytes <= 0 {
		t.Fatalf("entry bytes = %d", entries[0].Bytes)
	}
	if len(entries[0].Artifacts) != 2 {
		t.Fatalf("artifacts = %v", entries[0].Artifacts)
	}
}

// TestListSkipsHalfWrittenCaptures: a staging directory and a directory
// with no meta.json are both captures that never landed, and a watcher —
// or this listing — must not treat either as ingestible.
func TestListSkipsHalfWrittenCaptures(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	seed(t, inbox, "https://example.com/real", "Real", "2026-09-21T08:00:00Z")

	staging := filepath.Join(inbox, tmpPrefix+"123")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, MetaFile), []byte(`{"url":"https://example.com/partial"}`), 0o600); err != nil {
		t.Fatalf("write staging meta: %v", err)
	}
	noMeta := filepath.Join(inbox, "2026-09-21T09-00-00Z-example-com-nometa")
	if err := os.MkdirAll(noMeta, 0o700); err != nil {
		t.Fatalf("mkdir nometa: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	entries, err := List(inbox)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "Real" {
		t.Fatalf("entries = %+v, want only the landed capture", entries)
	}
}

// TestListCarriesTheWholeMeta is why Entry has a Meta field: a consumer
// filtering by collection or tag must not have to open and parse meta.json
// a second time, when listing the inbox already did exactly that.
func TestListCarriesTheWholeMeta(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	collection := "reading"
	w := &Writer{Inbox: inbox}
	if _, err := w.Write(&Envelope{
		Meta: Meta{
			URL:        "https://example.com/post",
			Title:      "A Post",
			CapturedAt: "2026-09-21T08:00:00Z",
			Collection: &collection,
			Tags:       []string{"research", "browser"},
			Source:     SourceCrawl,
		},
		Artifacts: map[string]Artifact{ArtifactHTML: Inline([]byte("<html></html>"))},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	entries, err := List(inbox)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	got := entries[0]
	if got.Meta.Collection == nil || *got.Meta.Collection != "reading" {
		t.Fatalf("entry.Meta.Collection = %v", got.Meta.Collection)
	}
	if len(got.Meta.Tags) != 2 || got.Meta.Tags[0] != "research" {
		t.Fatalf("entry.Meta.Tags = %v", got.Meta.Tags)
	}
	if got.Meta.Source != SourceCrawl {
		t.Fatalf("entry.Meta.Source = %q", got.Meta.Source)
	}
	if got.Meta.ContentHash == "" {
		t.Fatal("entry.Meta.ContentHash is empty")
	}
	// The display columns must still agree with the record behind them.
	if got.Title != got.Meta.Title || got.CapturedAt != got.Meta.CapturedAt {
		t.Fatalf("entry columns disagree with entry.Meta: %+v", got)
	}
	if got.Artifacts[0] != ArtifactHTML {
		t.Fatalf("artifacts = %v", got.Artifacts)
	}
}

func TestReadMeta(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	dir := seed(t, inbox, "https://example.com/post", "A Post", "2026-09-21T08:00:00Z")

	meta, err := ReadMeta(dir)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Title != "A Post" || meta.DedupeURL() != "https://example.com/post" {
		t.Fatalf("meta = %+v", meta)
	}
}

// TestReadMetaOnANonCapture: the error has to say "not a capture" in a form
// a caller can branch on, which is what os.ErrNotExist is for.
func TestReadMetaOnANonCapture(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadMeta(dir)
	if err == nil {
		t.Fatal("ReadMeta on a directory with no meta.json should fail")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want it to wrap os.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), MetaFile) {
		t.Fatalf("err = %v, want it to name %s", err, MetaFile)
	}
}

func TestReadMetaOnUnreadableJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := ReadMeta(dir)
	if err == nil {
		t.Fatal("ReadMeta on malformed meta.json should fail")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("a malformed capture must not look like a missing one")
	}
}

func TestListMissingInboxIsEmpty(t *testing.T) {
	entries, err := List(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v", entries)
	}
}

func TestDefaultInboxHonoursOverrides(t *testing.T) {
	t.Setenv(HomeEnv, filepath.Join("/somewhere", "mm"))
	t.Setenv(InboxEnv, "")
	if got, want := DefaultInbox(), filepath.Join("/somewhere", "mm", "inbox"); got != want {
		t.Fatalf("DefaultInbox = %q, want %q", got, want)
	}
	t.Setenv(InboxEnv, "/elsewhere/box")
	if got := DefaultInbox(); got != "/elsewhere/box" {
		t.Fatalf("DefaultInbox = %q", got)
	}
}

func TestDefaultInboxIsUnderMonomindHome(t *testing.T) {
	t.Setenv(HomeEnv, "")
	t.Setenv(InboxEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := DefaultInbox(), filepath.Join(home, ".monomind", "inbox"); got != want {
		t.Fatalf("DefaultInbox = %q, want %q", got, want)
	}
}

func TestReadMetaRefusesAnOversizeMetaFile(t *testing.T) {
	dir := t.TempDir()
	pad := strings.Repeat("x", MaxMetaFileBytes+10)
	if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte(`{"url":"https://a.test/","title":"`+pad+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMeta(dir); err == nil {
		t.Fatal("an oversize meta.json must not be read as a capture")
	}
	if _, err := ReadCapped(filepath.Join(dir, MetaFile), MaxMetaFileBytes); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadCapped err = %v, want ErrTooLarge", err)
	}
}
