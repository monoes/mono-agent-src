package vault

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// writeNew never replaces what is already at its path, a file or a symlink
// (O_EXCL), and never writes through a symlink to its target.
func TestWriteNewNeverReplacesAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "img-001.pdf")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(file, []byte("new")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("writeNew over a file: %v, want ErrExist", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "keep" {
		t.Errorf("the file was written: %q", b)
	}

	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "img-002.pdf")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if err := writeNew(link, []byte("new")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("writeNew over a symlink: %v, want ErrExist", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "target" {
		t.Errorf("the symlink's target was written: %q", b)
	}
}

// SaveBytes refuses an extension that is not a plain ".ext": the stored name
// is joined onto the vault folder, so one with a separator could reach out of it.
func TestSaveBytesRefusesABadExtension(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p-work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"/../../escape.pdf", "pdf", `.a\b`, "..", "", ".a/b"} {
		_, err := SaveBytes(context.Background(), db.DB, "p-work", SaveInput{Data: []byte("x"), Ext: ext, ClientID: "c-" + ext, Source: "test"})
		if err == nil {
			t.Errorf("extension %q was taken", ext)
		}
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM vault_images`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows written for bad extensions", n)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "profiles", "p-work", ".monoagent", "escape.pdf")); !os.IsNotExist(err) {
		t.Errorf("a file was written outside the vault folder: %v", err)
	}
}
