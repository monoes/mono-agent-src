package capturecli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/capture"
)

// profileFixture builds a disposable home with a monoagent database holding
// two profiles, and returns the config a capture command would run with.
func profileFixture(t *testing.T) (*testConfig, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(capture.InboxEnv, filepath.Join(home, "default-inbox"))

	dbPath := filepath.Join(home, ".monoagent", "monoagent.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT, created_at TEXT, root_dir TEXT)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO profiles (id, name, created_at) VALUES ('p-work', 'Work', '2026-01-01')`,
		`INSERT INTO profiles (id, name, created_at) VALUES ('p-home', 'Personal', '2026-02-01')`,
		`INSERT INTO settings (key, value) VALUES ('active_profile_id', 'p-work')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	db.Close()

	return &testConfig{DBPath: dbPath}, home
}

// seedProfileCapture writes one capture through the profile routing, so the
// test exercises the same path a real capture takes.
func seedProfileCapture(t *testing.T, profile, url, title, at string) string {
	t.Helper()
	w := &capture.Writer{Now: func() time.Time { return time.Now() }}
	res, err := w.Write(&capture.Envelope{
		Meta: capture.Meta{URL: url, Title: title, CapturedAt: at, Profile: profile},
		Artifacts: map[string]capture.Artifact{
			capture.ArtifactReadable: capture.Inline([]byte("# body")),
		},
	})
	if err != nil {
		t.Fatalf("seed %s: %v", url, err)
	}
	return res.Path
}

func TestCaptureListProfile(t *testing.T) {
	cfg, _ := profileFixture(t)
	seedProfileCapture(t, "p-work", "https://example.com/work", "Work Page", "2026-09-21T08:00:00Z")
	seedProfileCapture(t, "p-home", "https://example.com/home", "Home Page", "2026-09-21T09:00:00Z")

	cfg.ProfileID = "p-work"
	out, err := runCaptureCmd(t, cfg, "list")
	if err != nil {
		t.Fatalf("capture list --profile p-work: %v", err)
	}
	if !strings.Contains(out, "Work Page") {
		t.Errorf("work capture missing:\n%s", out)
	}
	if strings.Contains(out, "Home Page") {
		t.Errorf("a capture from another profile leaked into the listing:\n%s", out)
	}
	if !strings.Contains(out, "PROFILE") || !strings.Contains(out, "Work") {
		t.Errorf("listing does not name the profile:\n%s", out)
	}

	// The name works as well as the id — the same as `profile switch`.
	cfg.ProfileID = "Personal"
	out, err = runCaptureCmd(t, cfg, "list")
	if err != nil {
		t.Fatalf("capture list --profile Personal: %v", err)
	}
	if !strings.Contains(out, "Home Page") || strings.Contains(out, "Work Page") {
		t.Errorf("listing by name = \n%s", out)
	}
}

func TestCaptureListProfileJSON(t *testing.T) {
	cfg, _ := profileFixture(t)
	seedProfileCapture(t, "p-work", "https://example.com/work", "Work Page", "2026-09-21T08:00:00Z")

	cfg.ProfileID = "p-work"
	cfg.JSONOutput = true
	out, err := runCaptureCmd(t, cfg, "list")
	if err != nil {
		t.Fatalf("capture list --json: %v", err)
	}
	var entries []capture.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Profile != "p-work" {
		t.Errorf("entry.profile = %q, want p-work", entries[0].Profile)
	}
	if entries[0].Meta.Profile != "p-work" {
		t.Errorf("entry.meta.profile = %q, want p-work", entries[0].Meta.Profile)
	}
}

// --all-profiles is how you see the whole library at once, with each
// capture saying which brain it belongs to.
func TestCaptureListAllProfiles(t *testing.T) {
	cfg, _ := profileFixture(t)
	seedProfileCapture(t, "p-work", "https://example.com/work", "Work Page", "2026-09-21T08:00:00Z")
	seedProfileCapture(t, "p-home", "https://example.com/home", "Home Page", "2026-09-21T09:00:00Z")
	seedProfileCapture(t, "", "https://example.com/plain", "Plain Page", "2026-09-21T10:00:00Z")

	cfg.JSONOutput = true
	out, err := runCaptureCmd(t, cfg, "list", "--all-profiles")
	if err != nil {
		t.Fatalf("capture list --all-profiles: %v", err)
	}
	var entries []capture.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3:\n%s", len(entries), out)
	}
	// Newest first, across inboxes.
	if entries[0].Title != "Plain Page" || entries[2].Title != "Work Page" {
		t.Errorf("entries are not newest-first: %+v", entries)
	}
	byProfile := map[string]string{}
	for _, e := range entries {
		byProfile[e.Profile] = e.Title
	}
	if byProfile["p-work"] != "Work Page" || byProfile["p-home"] != "Home Page" || byProfile[""] != "Plain Page" {
		t.Errorf("profiles = %+v", byProfile)
	}
}

func TestCaptureListUnknownProfile(t *testing.T) {
	cfg, _ := profileFixture(t)
	cfg.ProfileID = "nope"
	if _, err := runCaptureCmd(t, cfg, "list"); err == nil {
		t.Fatal("an unknown profile should be an error, not an empty listing")
	}
}

// hostileProfileIDs are ids that must never become a path.
var hostileProfileIDs = []string{"../evil", "../../etc", `..\evil`, "p-work/..", "p-work/../../evil"}

// A hostile profile id gets nowhere. There are three gates between
// `--profile` and a directory, and this checks all three, because the
// first one alone hides the other two: an id that matches no row in the
// database is refused before any path is built, so a test that only runs
// the CLI would stay green with the path guard deleted.
func TestCaptureListRejectsTraversalProfile(t *testing.T) {
	cfg, home := profileFixture(t)

	// 1. The CLI refuses it rather than listing an empty directory.
	for _, id := range hostileProfileIDs {
		cfg.ProfileID = id
		if _, err := runCaptureCmd(t, cfg, "list"); err == nil {
			t.Errorf("--profile %q was accepted", id)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "evil")); err == nil {
		t.Error("a traversal id created something outside the profiles root")
	}

	// 2. A hostile id cannot be laundered through the database either: a
	// row holding one is not a profile, so the lookup never matches it.
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, id := range hostileProfileIDs {
		if _, err := db.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Hostile %d", i), "2026-03-01"); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range hostileProfileIDs {
		for _, want := range []string{id, fmt.Sprintf("Hostile %d", i)} {
			cfg.ProfileID = want
			if _, err := runCaptureCmd(t, cfg, "list"); err == nil {
				t.Errorf("a profile row with id %q was resolved by %q", id, want)
			}
		}
	}
	cfg.ProfileID = ""
	cfg.JSONOutput = true
	out, err := runCaptureCmd(t, cfg, "list", "--all-profiles")
	if err != nil {
		t.Fatalf("capture list --all-profiles: %v", err)
	}
	for _, id := range hostileProfileIDs {
		if strings.Contains(out, id) {
			t.Errorf("a hostile profile id reached the listing:\n%s", out)
		}
	}

	// 3. And the guard that all of the above leans on: the id never
	// becomes an inbox path, whatever asked for it.
	for _, id := range hostileProfileIDs {
		if got, err := capture.ProfileInbox(id); err == nil {
			t.Errorf("capture.ProfileInbox(%q) = %q, want a refusal", id, got)
		}
	}
}

func TestCaptureListOutStillWins(t *testing.T) {
	cfg, _ := profileFixture(t)
	inbox := seedInbox(t, capture.Meta{URL: "https://example.com/out", Title: "Out Page", CapturedAt: "2026-09-21T08:00:00Z"})

	cfg.ProfileID = "p-work"
	out, err := runCaptureCmd(t, cfg, "list", "--out", inbox)
	if err != nil {
		t.Fatalf("capture list --out: %v", err)
	}
	if !strings.Contains(out, "Out Page") {
		t.Errorf("--out did not win over --profile:\n%s", out)
	}
}

// Without a profile and without --all-profiles, `capture list` is exactly
// what it was: the default inbox, no profile column.
func TestCaptureListDefaultUnchanged(t *testing.T) {
	profileFixture(t)
	seedProfileCapture(t, "", "https://example.com/plain", "Plain Page", "2026-09-21T10:00:00Z")
	seedProfileCapture(t, "p-work", "https://example.com/work", "Work Page", "2026-09-21T08:00:00Z")

	out, err := runCaptureCmd(t, &testConfig{}, "list")
	if err != nil {
		t.Fatalf("capture list: %v", err)
	}
	if !strings.Contains(out, "Plain Page") {
		t.Errorf("default listing lost its captures:\n%s", out)
	}
	if strings.Contains(out, "Work Page") {
		t.Errorf("a profiled capture appeared in the default inbox:\n%s", out)
	}
	if strings.Contains(out, "PROFILE") {
		t.Errorf("the default listing grew a profile column:\n%s", out)
	}
}
