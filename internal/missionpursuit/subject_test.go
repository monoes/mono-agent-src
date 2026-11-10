package missionpursuit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// The digest the goal answer compares is exactly the tree digest the verifier records, and a change to the project changes it.
func TestSubjectDigestIsTheTreeDigest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := finish.DigestTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SubjectDigest(root)
	if err != nil || got != want.Digest {
		t.Fatalf("SubjectDigest = %q, %v; want %q", got, err, want.Digest)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a // x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, _ := SubjectDigest(root); again == got {
		t.Error("an edited project has the same digest")
	}
	if _, err := SubjectDigest(filepath.Join(root, "missing")); err == nil {
		t.Error("a missing folder has a digest")
	}
}
