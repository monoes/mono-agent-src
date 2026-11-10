package main

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The app's read handle opens the file at the given path even when a folder name holds ?, # or %.
func TestOpenReadOnlyDBOddPath(t *testing.T) {
	for _, dir := range []string{"a#b", "a?mode=memory", "a%41b", "a b"} {
		path := filepath.Join(t.TempDir(), dir, "monoagent.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		w, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Exec(`CREATE TABLE t (v INTEGER); INSERT INTO t VALUES (7)`); err != nil {
			t.Fatal(err)
		}
		w.Close()
		db, err := openReadOnlyDB(path)
		if err != nil {
			t.Fatalf("%q: %v", dir, err)
		}
		var v int
		if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != 7 {
			t.Fatalf("%q: v=%d err=%v", dir, v, err)
		}
		if _, err := db.Exec(`INSERT INTO t VALUES (8)`); err == nil {
			t.Fatalf("%q: write succeeded on the read-only handle", dir)
		}
		db.Close()
	}
}
