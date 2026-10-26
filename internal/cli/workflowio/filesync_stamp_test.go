package workflowio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

const ownerlessFile = `{"id":"w-legacy","name":"legacy","version":1,"is_active":false,"nodes":[{"id":"n1","type":"trigger.manual","name":"Start","position":{"x":1,"y":2},"config":{"k":"v"}}],"connections":[],"extra_key":{"keep":true}}`
const ownedFile = `{"id":"w-owned","name":"owned","version":1,"profile_id":"other","nodes":[],"connections":[]}`

func writeStampFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"w-legacy.json": ownerlessFile,
		"w-owned.json":  ownedFile,
		"notes.json":    `{"hello":"world"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fileDoc(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s no longer parses: %v", path, err)
	}
	return doc
}

func profileOfFile(t *testing.T, path string) string {
	t.Helper()
	var p string
	_ = json.Unmarshal(fileDoc(t, path)["profile_id"], &p)
	return p
}

// A workflow file with no profile_id is stamped once with the profile 'default'
// (when it exists); owned files and non-workflow JSON are untouched, nothing
// else in the stamped file changes, and a second run is a no-op.
func TestStampFileWorkflowsGivesOwnerlessFilesTheLegacyProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('other', 'Other', '2020-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	dir := writeStampFixture(t)

	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if got := profileOfFile(t, filepath.Join(dir, "w-legacy.json")); got != "default" {
		t.Errorf("ownerless file stamped %q, want default (it exists, though 'other' is older)", got)
	}
	if got := profileOfFile(t, filepath.Join(dir, "w-owned.json")); got != "other" {
		t.Errorf("owned file changed to %q", got)
	}
	notes, _ := os.ReadFile(filepath.Join(dir, "notes.json"))
	if string(notes) != `{"hello":"world"}` {
		t.Errorf("non-workflow json was rewritten: %s", notes)
	}
	doc := fileDoc(t, filepath.Join(dir, "w-legacy.json"))
	for _, key := range []string{"id", "name", "nodes", "connections", "extra_key"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("stamping lost key %q", key)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}

	before, _ := os.ReadFile(filepath.Join(dir, "w-legacy.json"))
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "w-legacy.json"))
	if string(before) != string(after) {
		t.Error("second run rewrote an already stamped file")
	}
}

// With 'default' absent the oldest profile inherits; with no profiles nothing
// is touched.
func TestStampFileWorkflowsFallsBackToTheOldestProfileOrNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`DELETE FROM profiles`,
	} {
		if _, err := db.DB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	dir := writeStampFixture(t)
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if got := profileOfFile(t, filepath.Join(dir, "w-legacy.json")); got != "" {
		t.Errorf("no profiles exist, yet the file was stamped %q", got)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('newer', 'N', '2026-05-01 00:00:00'), ('oldest', 'O', '2026-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if got := profileOfFile(t, filepath.Join(dir, "w-legacy.json")); got != "oldest" {
		t.Errorf("stamped %q, want the oldest profile", got)
	}
}

// The sync runs the stamp first, so an ownerless file reaches SQLite under its
// inherited profile instead of being refused.
func TestSyncStampsBeforeMirroring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := writeStampFixture(t)
	if _, err := SyncFileWorkflowsToSQL(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	var profile string
	if err := db.DB.QueryRow(`SELECT profile_id FROM workflows WHERE id = 'w-legacy'`).Scan(&profile); err != nil || profile != "default" {
		t.Fatalf("mirrored profile = %q, %v; want default", profile, err)
	}
}

// The owner recorded in the workflow's SQLite mirror row wins over the
// default/oldest rule: a file whose row belongs to 'work' is stamped 'work'.
func TestStampFileWorkflowsTakesTheOwnerFromTheSQLiteRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('work', 'Work', '2026-05-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('w-legacy', 'legacy', 'work')`); err != nil {
		t.Fatal(err)
	}
	dir := writeStampFixture(t)
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if got := profileOfFile(t, filepath.Join(dir, "w-legacy.json")); got != "work" {
		t.Errorf("file stamped %q, want work (its SQLite row's owner)", got)
	}
}

// With no profile nothing can be stamped, so the pass does not even list the folder (a folder it
// could not list would be an error otherwise).
func TestStampFileWorkflowsDoesNotScanTheFolderWithoutAProfile(t *testing.T) {
	t.Parallel()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec("DEL" + "ETE FROM profiles"); err != nil {
		t.Fatal(err)
	}
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StampFileWorkflows(context.Background(), db.DB, notADir); err != nil {
		t.Fatalf("the folder was scanned although no profile exists: %v", err)
	}
}
