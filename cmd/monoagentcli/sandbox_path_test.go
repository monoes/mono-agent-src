package main

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The fallback reader opens the file at the given path even when a folder name holds ?, # or %.
func TestReadSandboxFallbackFileOddPath(t *testing.T) {
	for _, dir := range []string{"a#b", "a?mode=memory", "a%41b", "a b"} {
		path := filepath.Join(t.TempDir(), dir, "monoagent.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		w, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Exec(`CREATE TABLE configs (name TEXT PRIMARY KEY, config_data TEXT NOT NULL);
			INSERT INTO configs VALUES ('sandbox.fallback', '"fail"')`); err != nil {
			t.Fatal(err)
		}
		w.Close()
		got, err := readSandboxFallbackFile(path)
		if err != nil || got != "fail" {
			t.Fatalf("%q: got %q, %v", dir, got, err)
		}
	}
}
