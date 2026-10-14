package profile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/storage"
)

// newCaptureTestProfile creates a profile in the test database and
// returns its id.
func newCaptureTestProfile(t *testing.T, dbPath, name string) string {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := "711ef586-9f4b-4b1f-b2fd-cad23eec0a03"
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func runProfileCmdAs(t *testing.T, dbPath, profileID string, args ...string) (string, error) {
	t.Helper()
	cfg := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: profileID}
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return out.String(), err
}

func writeTestCapture(t *testing.T, profileID, name string) {
	t.Helper()
	inbox, err := capture.ProfileInbox(profileID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(inbox, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"title":"` + name + `","url":"https://example.com/` + name + `","capturedAt":"2026-09-28T15:00:13Z","source":"extension","profile":"` + profileID + `"}`
	if err := os.WriteFile(filepath.Join(dir, capture.MetaFile), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readable.md"), []byte("# "+name), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestProfileDocumentsIndexAllBackfillsCaptures is the backfill: every
// unindexed capture of the profile goes to its capture store, once.
func TestProfileDocumentsIndexAllBackfillsCaptures(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	captureTestProfile := newCaptureTestProfile(t, dbPath, "work")
	setFakeMonomindOnPathCLI(t)
	log := filepath.Join(t.TempDir(), "mcp.log")
	t.Setenv("FAKE_MCP_LOG", log)
	writeTestCapture(t, captureTestProfile, "a")
	writeTestCapture(t, captureTestProfile, "b")
	writeTestCapture(t, "other", "c")

	out, err := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "index", "--all")
	if err != nil {
		t.Fatalf("index --all: %v (%s)", err, out)
	}
	var got struct {
		Profiles []struct {
			Profile string `json:"profile"`
			Indexed int    `json:"indexed"`
			Failed  int    `json:"failed"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].Indexed != 2 || got.Profiles[0].Failed != 0 {
		t.Fatalf("want 2 captures indexed, got %s", out)
	}
	raw, _ := os.ReadFile(log)
	if n := strings.Count(string(raw), `"scope":"profile:`+captureTestProfile+`"`); n != 2 {
		t.Fatalf("want 2 ingests into the profile's capture scope, got %d:\n%s", n, raw)
	}
	if strings.Contains(string(raw), "profile:other") {
		t.Fatalf("indexed another profile's capture:\n%s", raw)
	}

	// The rows say Indexed, and a second run has nothing to do.
	list, _ := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "list")
	if strings.Count(list, `"Indexed": true`) != 2 {
		t.Fatalf("rows not marked indexed:\n%s", list)
	}
	out, err = runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "index", "--all")
	if err != nil || !strings.Contains(out, `"indexed": 0`) {
		t.Fatalf("second run re-indexed: %v\n%s", err, out)
	}
}

func TestProfileDocumentsIndexAllReportsFailures(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	captureTestProfile := newCaptureTestProfile(t, dbPath, "work")
	setFakeMonomindOnPathCLI(t)
	t.Setenv("INGEST_TOOL_ERROR", "1")
	writeTestCapture(t, captureTestProfile, "a")

	out, err := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "index", "--all")
	if err == nil {
		t.Fatalf("a failed capture must fail the backfill, got success:\n%s", out)
	}
	if !strings.Contains(out, `"failed": 1`) {
		t.Fatalf("want the failure counted in the JSON, got:\n%s", out)
	}
	list, _ := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "list")
	if !strings.Contains(list, "Absolute path must not escape") {
		t.Fatalf("the error must be on the row:\n%s", list)
	}
}

func TestProfileDocumentsIndexOneCaptureUsesCaptureStore(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	captureTestProfile := newCaptureTestProfile(t, dbPath, "work")
	setFakeMonomindOnPathCLI(t)
	log := filepath.Join(t.TempDir(), "mcp.log")
	t.Setenv("FAKE_MCP_LOG", log)
	writeTestCapture(t, captureTestProfile, "a")

	list, err := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "list")
	if err != nil {
		t.Fatal(err)
	}
	id := extractJSONStringField(t, list, "ID")
	out, err := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "index", id)
	if err != nil || !strings.Contains(out, `"indexed": true`) {
		t.Fatalf("index %s: %v\n%s", id, err, out)
	}
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), `"scope":"profile:`+captureTestProfile+`"`) {
		t.Fatalf("a capture row must go to the capture scope:\n%s", raw)
	}
}

func TestProfileDocumentsIndexArgs(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	captureTestProfile := newCaptureTestProfile(t, dbPath, "work")
	if _, err := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "index"); err == nil {
		t.Fatal("no id and no --all must be refused")
	}
	if _, err := runProfileCmdAs(t, dbPath, captureTestProfile, "documents", "index", "doc-1", "--all"); err == nil {
		t.Fatal("an id with --all must be refused")
	}
}
