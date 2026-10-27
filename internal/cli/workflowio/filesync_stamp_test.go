package workflowio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
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

// A claim another process records while the pass runs is noticed: from then on a file with no rows
// is not eligible, and a workflow with a change recorded never is.
func TestStampEligibilityFollowsTheClaimAndOpenChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	eligible := stampEligible(ctx, db.DB)
	if !eligible("w-legacy") {
		t.Fatal("before the claim every ownerless file may need an owner")
	}
	if _, err := db.DB.Exec(`INSERT INTO settings (key, value) VALUES ('workflow_files.legacy_import_done', '1')`); err != nil {
		t.Fatal(err)
	}
	if eligible("w-other") {
		t.Fatal("a second file with no rows was stamped after the claim was recorded between two files")
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('w-rows', 'r', 'default')`); err != nil {
		t.Fatal(err)
	}
	if !eligible("w-rows") {
		t.Fatal("a workflow with rows is eligible")
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('w-rows', 'save', 'unresolved', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if eligible("w-rows") {
		t.Fatal("a workflow with a change recorded was eligible")
	}
}

// A workflow with a change recorded is not stamped: the writer of that change owns its file.
func TestStampFileWorkflowsSkipsAWorkflowWithAChangeRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := writeStampFixture(t)
	if _, err := db.DB.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('w-legacy', 'save', 'pending', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if got := profileOfFile(t, filepath.Join(dir, "w-legacy.json")); got != "" {
		t.Fatalf("a file with a change recorded was stamped %q", got)
	}
}

// A publisher that edits the file while the stamp pass is about to look at it is not overwritten with
// a stale document: the pass waits for the lock, then sees the edit and stamps that.
func TestStampWaitsForAWriterHoldingTheLockAndKeepsItsEdit(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := writeStampFixture(t)
	path := filepath.Join(dir, "w-legacy.json")
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- workflow.WithPublicationLock(ctx, db.DB, "w-legacy", func() error {
			close(started)
			time.Sleep(400 * time.Millisecond)
			return os.WriteFile(path, []byte(`{"id":"w-legacy","name":"EDITED","nodes":[],"connections":[]}`), 0o600)
		})
	}()
	<-started
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "EDITED") || profileOfFile(t, path) != "default" {
		t.Fatalf("the edit was lost or the file not stamped:\n%s", b)
	}
}

// A pass over ineligible files never attempts a publication lock, even while another
// connection holds the database's write lock. The name cap is tested separately.
func TestStampPassOverIneligibleFilesTakesNoLock(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO settings (key, value) VALUES ('workflow_files.legacy_import_done', '1')`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // ownerless, no rows, claim done: none is eligible
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("w-%05d.json", i)), []byte(ownerlessFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	// BEGIN IMMEDIATE has acquired the write lock. Reject an attempt at the hook,
	// before SQLite's busy wait, so a regression fails without depending on time.
	old := stampTestHook
	t.Cleanup(func() { stampTestHook = old })
	stampTestHook = func(id string) {
		t.Fatalf("ineligible file %q attempted a publication lock", id)
	}
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
}

// A claim another process records between two files of one pass, seen through the real pass: neither
// file is stamped.
func TestStampPassNoticesAClaimRecordedBetweenFiles(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a-one.json", "b-two.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(ownerlessFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stampTestHook = func(string) {
		// The claim lands after the pass decided the first file was eligible, before it is locked.
		_, _ = db.DB.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('workflow_files.legacy_import_done', '1')`)
	}
	t.Cleanup(func() { stampTestHook = nil })
	if err := StampFileWorkflows(ctx, db.DB, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a-one.json", "b-two.json"} {
		if got := profileOfFile(t, filepath.Join(dir, name)); got != "" {
			t.Errorf("%s was stamped %q after the claim was recorded", name, got)
		}
	}
}

// A real busy write lock ends the pass, rather than charging a lock wait to
// every eligible file. The next pass handles all the files after release.
func TestStampPassStopsAtFirstBusyEligibleFile(t *testing.T) {
	ctx := context.Background()
	path := testdb.Path(t)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// WAL is already set by NewDatabase. Each connection in this separate
	// pool uses a short busy wait; correctness does not depend on elapsed time.
	pass, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(1)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer pass.Close()
	if err := pass.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ids := []string{"a-first", "b-next", "c-last"}
	originals := map[string]string{}
	for _, id := range ids {
		raw := fmt.Sprintf(`{"id":%q,"nodes":[],"connections":[]}`, id)
		originals[id] = raw
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	old := stampTestHook
	t.Cleanup(func() { stampTestHook = old })
	var attempted []string
	stampTestHook = func(id string) { attempted = append(attempted, id) }
	if err := StampFileWorkflows(ctx, pass, dir); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(attempted, ids[:1]) {
		t.Fatalf("lock attempts=%v, want only the first busy file %v", attempted, ids[:1])
	}
	for _, id := range ids {
		raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err != nil || string(raw) != originals[id] {
			t.Fatalf("busy pass touched %s: %s (%v)", id, raw, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	attempted = nil
	if err := StampFileWorkflows(ctx, pass, dir); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(attempted, ids) {
		t.Fatalf("next pass attempts=%v, want %v", attempted, ids)
	}
	for _, id := range ids {
		if got := profileOfFile(t, filepath.Join(dir, id+".json")); got != "default" {
			t.Errorf("next pass left %s profile=%q", id, got)
		}
	}
}
