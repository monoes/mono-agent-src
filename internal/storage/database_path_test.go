package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A folder name with a URI metacharacter is part of the name: the file opened is the file at the path, with the
// connection options applied (a "#" used to cut them off, a "?" to start foreign ones, a "%41" to open another file).
func TestNewDatabaseOpensThePathItWasGiven(t *testing.T) {
	for _, dir := range []string{"a#b", "a?b", "a%41b", "a%b", "a b", "a&b", "a#b?c%d e"} {
		t.Run(strings.NewReplacer("#", "H", "?", "Q", "%", "P", " ", "S", "&", "A").Replace(dir), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, dir, "monoagent.db")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			d, err := NewDatabase(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the file at the given path was not opened: %v", err)
			}
			var fk, busy int
			var mode string
			if err := d.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
				t.Fatal(err)
			}
			if fk != 1 || busy != 5000 || mode != "wal" {
				t.Fatalf("options dropped: foreign_keys=%d busy_timeout=%d journal_mode=%s", fk, busy, mode)
			}
			// Nothing was created next to the folder under a decoded or truncated name.
			entries, _ := os.ReadDir(root)
			if len(entries) != 1 || entries[0].Name() != dir {
				t.Fatalf("stray entries in %s: %v", root, entries)
			}
		})
	}
}
