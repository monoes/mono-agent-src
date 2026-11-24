package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// A planted symlink named like an envelope must not turn the recursive delete
// into a delete of what it points at.
func TestRemoveCaptureDirRefusesSymlinkedEnvelope(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "precious")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "page.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(base, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(inbox, "x")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := removeCaptureDir(link, filepath.Join(link, "page.html")); err == nil {
		t.Fatal("removeCaptureDir followed a symlink")
	}
	if _, err := os.Stat(filepath.Join(victim, "page.html")); err != nil {
		t.Fatalf("victim was deleted: %v", err)
	}
}

func TestRemoveCaptureDirRemovesARealEnvelope(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	env := filepath.Join(inbox, "cap1")
	if err := os.MkdirAll(env, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env, "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCaptureDir(env, filepath.Join(env, "page.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env); !os.IsNotExist(err) {
		t.Fatalf("envelope still there: %v", err)
	}
}
