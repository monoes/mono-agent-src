package workflowbundle

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The atomic write keeps the mode a plain os.WriteFile gave: 0644 for a new file, the existing
// file's mode when it is replaced (R15-13).
func TestWriteFileAtomicMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.json")
	if err := WriteFileAtomic(fresh, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(fresh); fi.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode = %v, want 0644", fi.Mode().Perm())
	}
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(existing, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o640 {
		t.Fatalf("replaced file mode = %v, want 0640", fi.Mode().Perm())
	}
}

// A symlink at the path is written through, not replaced by a regular file.
func TestWriteFileAtomicKeepsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a regular file")
	}
	if b, _ := os.ReadFile(real); string(b) != "new" {
		t.Fatalf("target = %q, want written through", b)
	}
}
