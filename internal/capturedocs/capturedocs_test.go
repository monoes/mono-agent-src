package capturedocs_test

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/capturedocs"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

const profileID = "711ef586-9f4b-4b1f-b2fd-cad23eec0a03"

func newTestDB(t *testing.T) *storage.Database {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // ProfileInbox and the vault both resolve under $HOME
	// The vault syncs each file into the knowledge graph in the background:
	// run a no-op instead of the real monomind, and let it finish before HOME
	// is removed.
	t.Setenv("MONOMIND_BIN", "true")
	t.Cleanup(vault.Wait)
	t.Setenv(capture.InboxEnv, "")
	t.Setenv(capture.HomeEnv, "")
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "capturedocs-test.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// writeCapture lays down one landed envelope the way capture.Writer does:
// a directory holding meta.json plus the named artifacts.
func writeCapture(t *testing.T, inbox, name string, meta map[string]any, artifacts ...string) string {
	t.Helper()
	dir := filepath.Join(inbox, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, capture.MetaFile), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, a := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, a), []byte("content of "+a), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func meta(title, url, at string) map[string]any {
	return map[string]any{"title": title, "url": url, "capturedAt": at, "source": "extension", "profile": profileID, "tags": []string{}}
}

func listByDir(t *testing.T, db *storage.Database) map[string]vault.DocumentEntry {
	t.Helper()
	docs, err := vault.ListDocuments(context.Background(), db.DB, profileID)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	out := map[string]vault.DocumentEntry{}
	for _, d := range docs {
		out[d.CaptureDir] = d
	}
	return out
}

func TestPrimaryArtifact(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"page.mhtml", "page.pdf", "readable.md", "screenshot.png"}, "readable.md"},
		{[]string{"page.mhtml", "page.pdf", "screenshot.png"}, "page.pdf"},
		{[]string{"page.mhtml", "screenshot.png"}, "page.mhtml"},
		{[]string{"page.html"}, "page.html"},
		{[]string{"screenshot.png"}, "screenshot.png"},
		{[]string{"table.csv", "transcript.md"}, "transcript.md"},
		// A summary arrives after the capture; it must not take the row over.
		{[]string{"readable.md", "screenshot.png", "summary.json", "summary.md", "transcript.md"}, "readable.md"},
		{[]string{"screenshot.png", "summary.json", "summary.md"}, "summary.md"},
		{[]string{"table.csv", "zz.txt"}, "table.csv"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := capturedocs.PrimaryArtifact(c.in); got != c.want {
			t.Errorf("PrimaryArtifact(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSync_BackfillsOneRowPerCapture(t *testing.T) {
	db := newTestDB(t)
	inbox, err := capture.ProfileInbox(profileID)
	if err != nil {
		t.Fatal(err)
	}
	readable := writeCapture(t, inbox, "2026-09-22T10-20-19Z-example-com",
		meta("Example article", "https://example.com/a", "2026-09-22T10:20:19.798Z"),
		"page.mhtml", "page.pdf", "readable.md", "screenshot.png")
	mhtmlOnly := writeCapture(t, inbox, "2026-09-22T10-20-27Z-claude-ai",
		meta("Jev Picker Plan", "https://claude.ai/artifact/x", "2026-09-22T10:20:27.093Z"),
		"page.mhtml", "screenshot.png")
	// Staging directories and envelopes without meta.json are not captures.
	if err := os.MkdirAll(filepath.Join(inbox, ".tmp-capture-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(inbox, "half-written"), 0o700); err != nil {
		t.Fatal(err)
	}

	added, removed, errs := capturedocs.Sync(context.Background(), db.DB, profileID)
	if len(errs) != 0 || added != 2 || removed != 0 {
		t.Fatalf("Sync = added %d removed %d errs %v; want 2, 0, none", added, removed, errs)
	}

	rows := listByDir(t, db)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(rows), rows)
	}
	r := rows[readable]
	if r.Source != "extension" || r.Filename != "Example article" || r.URL != "https://example.com/a" {
		t.Errorf("readable capture row = %+v", r)
	}
	if r.Path != filepath.Join(readable, "readable.md") {
		t.Errorf("readable capture path = %q, want its readable.md", r.Path)
	}
	if r.CreatedAt != "2026-09-22 10:20:19" {
		t.Errorf("created_at = %q, want the capture time in created_at's shape", r.CreatedAt)
	}
	if m := rows[mhtmlOnly]; m.Path != filepath.Join(mhtmlOnly, "page.mhtml") || m.Filename != "Jev Picker Plan" {
		t.Errorf("mhtml-only capture row = %+v", m)
	}

	// A second sync changes nothing.
	added, removed, errs = capturedocs.Sync(context.Background(), db.DB, profileID)
	if len(errs) != 0 || added != 0 || removed != 0 {
		t.Fatalf("second Sync = added %d removed %d errs %v; want no change", added, removed, errs)
	}
}

func TestSync_RemovesVanishedCapturesOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := vault.ContextWithProfileID(context.Background(), profileID)
	inbox, _ := capture.ProfileInbox(profileID)
	gone := writeCapture(t, inbox, "a", meta("A", "https://a.test", "2026-09-22T10:00:00Z"), "readable.md")
	kept := writeCapture(t, inbox, "b", meta("B", "https://b.test", "2026-09-22T11:00:00Z"), "readable.md")

	upload := filepath.Join(t.TempDir(), "resume.txt")
	if err := os.WriteFile(upload, []byte("cv"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploadID, err := vault.RegisterDocument(ctx, db.DB, upload, "upload")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, errs := capturedocs.Sync(ctx, db.DB, profileID); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	added, removed, errs := capturedocs.Sync(ctx, db.DB, profileID)
	if len(errs) != 0 || added != 0 || removed != 1 {
		t.Fatalf("Sync after removal = added %d removed %d errs %v; want 0, 1", added, removed, errs)
	}
	rows := listByDir(t, db)
	if _, ok := rows[kept]; !ok {
		t.Errorf("remaining capture was dropped")
	}
	if _, ok := rows[gone]; ok {
		t.Errorf("vanished capture still listed")
	}
	if d, err := vault.GetDocument(ctx, db.DB, profileID, uploadID); err != nil || d == nil {
		t.Errorf("uploaded document must survive a capture sync: %v %v", d, err)
	}
}

func TestSync_OtherProfilesAndSharedInboxAreNotListed(t *testing.T) {
	db := newTestDB(t)
	other, _ := capture.ProfileInbox("someone-else")
	writeCapture(t, other, "x", meta("Theirs", "https://x.test", "2026-09-22T10:00:00Z"), "readable.md")
	writeCapture(t, capture.DefaultInbox(), "y", meta("Shared", "https://y.test", "2026-09-22T10:00:00Z"), "readable.md")

	if _, _, errs := capturedocs.Sync(context.Background(), db.DB, profileID); len(errs) != 0 {
		t.Fatal(errs)
	}
	if rows := listByDir(t, db); len(rows) != 0 {
		t.Fatalf("want no rows for %s, got %+v", profileID, rows)
	}
	// No profile at all is a no-op, never the shared inbox.
	if added, _, errs := capturedocs.Sync(context.Background(), db.DB, ""); added != 0 || len(errs) != 0 {
		t.Fatalf("Sync(\"\") = added %d errs %v", added, errs)
	}
}

func TestDeleteDocument_RemovesTheWholeCapture(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	inbox, _ := capture.ProfileInbox(profileID)
	dir := writeCapture(t, inbox, "c", meta("C", "https://c.test", "2026-09-22T10:00:00Z"), "readable.md", "page.mhtml", "screenshot.png")
	if _, _, errs := capturedocs.Sync(ctx, db.DB, profileID); len(errs) != 0 {
		t.Fatal(errs)
	}
	row := listByDir(t, db)[dir]
	if row.ID == "" {
		t.Fatal("capture was not registered")
	}
	if err := vault.DeleteDocument(ctx, db.DB, profileID, row.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("capture directory should be gone, stat err = %v", err)
	}
	// And it does not come back on the next sync.
	if added, _, _ := capturedocs.Sync(ctx, db.DB, profileID); added != 0 {
		t.Fatalf("deleted capture was re-registered")
	}
}

func TestDeleteDocument_RefusesANonEnvelopeCaptureDir(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	notInbox := t.TempDir()
	keep := filepath.Join(notInbox, "precious.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A row whose capture_dir does not look like an envelope (as a
	// corrupted or hand-edited DB could hold) must not trigger RemoveAll.
	if _, _, errs := vault.ReconcileCaptureDocuments(ctx, db.DB, profileID, []vault.CaptureDocument{{
		Dir: notInbox, Path: keep, Title: "t", Source: "extension",
	}}); len(errs) != 0 {
		t.Fatal(errs)
	}
	row := listByDir(t, db)[notInbox]
	if err := vault.DeleteDocument(ctx, db.DB, profileID, row.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("file outside an inbox was removed: %v", err)
	}
}

func TestSync_SkipsActivityRecordings(t *testing.T) {
	db := newTestDB(t)
	inbox, err := capture.ProfileInbox(profileID)
	if err != nil {
		t.Fatal(err)
	}
	page := writeCapture(t, inbox, "2026-09-22T10-20-19Z-example-com",
		meta("Example article", "https://example.com/a", "2026-09-22T10:20:19Z"), "readable.md")
	rec := meta("Recording", "https://example.com/login", "2026-09-22T11:00:00Z")
	rec["source"] = capture.SourceRecording
	writeCapture(t, inbox, "2026-09-22T11-00-00Z-example-com-login", rec, "events.jsonl", "dom-e1.html")

	added, _, errs := capturedocs.Sync(context.Background(), db.DB, profileID)
	if len(errs) != 0 || added != 1 {
		t.Fatalf("Sync = added %d errs %v; want only the page", added, errs)
	}
	if rows := listByDir(t, db); len(rows) != 1 || rows[page].Filename != "Example article" {
		t.Fatalf("rows = %+v", rows)
	}
}
