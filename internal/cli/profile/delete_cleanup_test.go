package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/attachments"
	"github.com/monoes/mono-agent/internal/storage"
)

func seedMessage(t *testing.T, db *storage.Database, profileID, id string, files ...string) {
	t.Helper()
	var atts []map[string]string
	for _, f := range files {
		atts = append(atts, map[string]string{"path": f})
	}
	md, _ := json.Marshal(map[string]any{"attachments": atts})
	if _, err := db.DB.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES (?, ?, 'x', ?)`, "p-"+id, "u-"+id, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO person_messages (id, person_id, source, external_id, direction, metadata, status, profile_id, created_at)
		VALUES (?, ?, 'outlook', ?, 'inbound', ?, 'sent', ?, '2026-10-01T00:00:00Z')`, id, "p-"+id, id, string(md), profileID); err != nil {
		t.Fatal(err)
	}
}

func writeAttachment(t *testing.T, msg, name string) string {
	t.Helper()
	p, err := attachments.Save(msg, name, []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProfileDeleteRemovesItsAttachmentsOnly(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")
	mine := writeAttachment(t, "m-mine", "a.pdf")
	shared := writeAttachment(t, "m-shared", "s.pdf")
	theirs := writeAttachment(t, "m-theirs", "t.pdf")
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, db, id, "m1", mine, shared, outside)
	seedMessage(t, db, "default", "m2", shared)
	seedMessage(t, db, "default", "m3", theirs)
	db.Close()

	out, err := runCaptured(t, dbPath, "delete", id, "--yes-delete-profile", "doomed")
	if err != nil {
		t.Fatal(err)
	}
	var res profileImpact
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("the deleted profile's attachment is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(mine)); !os.IsNotExist(err) {
		t.Errorf("its emptied message folder is still there: %v", err)
	}
	for name, p := range map[string]string{"shared with another profile": shared, "another profile's": theirs, "outside the attachments folder": outside} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("attachment %s was removed: %v", name, err)
		}
	}
	if res.AttachmentsRemoved != 1 {
		t.Errorf("attachments_removed = %d, want 1", res.AttachmentsRemoved)
	}
}

func TestProfileDeleteKeepAttachments(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")
	mine := writeAttachment(t, "m-keep", "a.pdf")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, db, id, "m1", mine)
	db.Close()
	if _, err := runCaptured(t, dbPath, "delete", id, "--yes-delete-profile", "doomed", "--keep-attachments"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("--keep-attachments removed the file: %v", err)
	}
}

func TestPruneBackupsKeepsNewestAndRecent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ages := []int{90, 80, 70, 60, 40, 5, 4} // days old, oldest first
	var paths []string
	for i, days := range ages {
		p := filepath.Join(dir, fmt.Sprintf("pre-delete-profile-%d.db", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(days) * 24 * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if n := pruneBackups(paths, paths[6], now); n != 4 {
		t.Fatalf("pruned %d, want 4 (the 90, 80, 70 and 60 day old ones)", n)
	}
	for i, p := range paths {
		_, err := os.Stat(p)
		if gone := os.IsNotExist(err); gone != (i < 4) {
			t.Errorf("backup %d (%d days old): gone = %v", i, ages[i], gone)
		}
	}
}

// removeAttachments never follows a symlink: a link in the attachments
// folder is skipped, and so is a file reached through a linked folder.
func TestRemoveAttachmentsDoesNotFollowSymlinks(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	dir := attachments.Dir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "m1"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "m1", "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "m2")); err != nil {
		t.Fatal(err)
	}
	n, _ := removeAttachments([]string{link, filepath.Join(dir, "m2", "secret.txt")})
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("a file outside the attachments folder was removed: %v", err)
	}
	if n != 0 {
		t.Errorf("removed %d, want 0", n)
	}
}

// A file a message that arrived after the list was made points at is not
// unlinked.
func TestUnreferencedDropsFilesARemainingMessageNames(t *testing.T) {
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "other")
	shared := writeAttachment(t, "m-shared", "a.pdf")
	free := writeAttachment(t, "m-free", "b.pdf")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedMessage(t, db, id, "late", shared)
	got, err := unreferenced(context.Background(), db.DB, []string{shared, free})
	if err != nil || len(got) != 1 || got[0] != free {
		t.Fatalf("unreferenced = %v, %v", got, err)
	}
}
