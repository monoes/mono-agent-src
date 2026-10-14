package profile

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestProfileDocumentsListIncludesBrowserCaptures: `documents list` is what
// the GUI's Documents page shows, and it must include the captures the
// extension saved into this profile's inbox — including ones saved before
// the list ever ran (backfill) — one row per capture, source "extension".
func TestProfileDocumentsListIncludesBrowserCaptures(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t) // sets its own HOME; ours must win
	home := t.TempDir()
	t.Setenv("HOME", home)
	const profile = "p-captures"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, 'Captures', '2026-01-01')`, profile); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := filepath.Join(home, ".monoagent", "profiles", profile, ".monomind", "inbox", "2026-09-22T10-20-19Z-example-com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"title":"Example article","url":"https://example.com/a","capturedAt":"2026-09-22T10:20:19Z","source":"extension","profile":"p-captures"}`
	for name, body := range map[string]string{"meta.json": meta, "page.mhtml": "mhtml", "screenshot.png": "png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	list := func() []struct{ ID, Filename, Source, URL, Path, CaptureDir string } {
		t.Helper()
		cfg := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: profile}
		cmd := NewCommand(testDeps(cfg))
		cmd.SetArgs([]string{"documents", "list"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("documents list: %v", err)
		}
		var docs []struct{ ID, Filename, Source, URL, Path, CaptureDir string }
		if err := json.Unmarshal(out.Bytes(), &docs); err != nil {
			t.Fatalf("decode %s: %v", out.String(), err)
		}
		return docs
	}

	docs := list()
	if len(docs) != 1 {
		t.Fatalf("want exactly one row for the capture, got %d: %+v", len(docs), docs)
	}
	d := docs[0]
	if d.Source != "extension" || d.Filename != "Example article" || d.URL != "https://example.com/a" ||
		d.Path != filepath.Join(dir, "page.mhtml") || d.CaptureDir != dir {
		t.Fatalf("capture row = %+v", d)
	}
	// Listing again does not duplicate it.
	if again := list(); len(again) != 1 || again[0].ID != d.ID {
		t.Fatalf("second list = %+v, want the same single row", again)
	}
}
