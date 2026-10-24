package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A symlink planted at the old, predictable temp name is neither followed nor truncated.
func TestAtomicWriteIgnoresAPlantedSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wf.json")
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Skip("no symlinks here")
	}
	if err := writeFileAtomic(path, []byte(`{"id":"wf"}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatalf("the symlink target was written through: %q", b)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"id":"wf"}` {
		t.Fatalf("the file = %q", b)
	}
	// The planted link is still there, untouched.
	if _, err := os.Lstat(path + ".tmp"); err != nil {
		t.Fatalf("the planted link was disturbed: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
}

// A write that fails leaves no temp file; a rename onto a directory fails the same way.
func TestAtomicWriteRemovesItsTempFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.json")
	if err := os.Mkdir(path, 0o700); err != nil { // the target is a directory: rename fails
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("x")); err == nil {
		t.Fatal("renaming over a non-empty directory succeeded")
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestConcurrentAtomicWritersNeverShareATempName(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writeFileAtomic(filepath.Join(dir, "wf.json"), []byte(strings.Repeat("a", 1000))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b, _ := os.ReadFile(filepath.Join(dir, "wf.json")); len(b) != 1000 {
		t.Fatalf("a torn file: %d bytes", len(b))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
