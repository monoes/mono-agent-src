package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestWalkFolderFiles(t *testing.T) {
	// A dot-named chosen folder is still walked; dot entries below it are not.
	root := filepath.Join(t.TempDir(), ".chosen")
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "a")
	write("sub/b.txt", "bb")
	write(".env", "secret")
	write("prod.env", "secret")
	write("id_rsa", "secret")
	write("creds.json", "secret")
	write("AGENTS.md", "tool config")
	write(".git/config", "x")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "o.txt"), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}

	w := walkFolderFiles(root)
	if w.over {
		t.Fatal("unexpectedly over cap")
	}
	var rel []string
	for _, f := range w.files {
		r, _ := filepath.Rel(root, f)
		rel = append(rel, r)
	}
	sort.Strings(rel)
	if len(rel) != 2 || rel[0] != "a.txt" || rel[1] != filepath.Join("sub", "b.txt") {
		t.Fatalf("files = %v", rel)
	}
	if w.total != 3 {
		t.Fatalf("total = %d", w.total)
	}
	if w.skippedN != 4 { // .env, .git, linkdir, link.txt
		t.Fatalf("skipped = %v", w.skipped)
	}
	if w.unsupported != 4 { // prod.env, id_rsa, creds.json, AGENTS.md
		t.Fatalf("unsupported = %d", w.unsupported)
	}
	if w.byExt["txt"] != 2 {
		t.Fatalf("byExt = %v", w.byExt)
	}
}
