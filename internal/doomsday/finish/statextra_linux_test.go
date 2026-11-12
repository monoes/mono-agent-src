package finish

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A rewrite that restores the size and the modification time still changes the manifest (the change time and inode moved).
// Revert proof: take statExtra out of the manifest line and this is red.
func TestTheManifestSeesARewriteThatKeepsSizeAndMtime(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a")
	if err := os.WriteFile(p, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	_ = os.Chtimes(p, mt, mt)
	before, err := StatManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(p, []byte("bravo"), 0o644); err != nil { // same size
		t.Fatal(err)
	}
	_ = os.Chtimes(p, mt, mt) // same mtime
	after, _ := StatManifest(dir)
	if before == after {
		t.Fatal("a same-size, same-mtime rewrite left the manifest unchanged")
	}
}
