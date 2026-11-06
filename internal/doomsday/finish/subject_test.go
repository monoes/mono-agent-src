package finish

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func write(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDigestIsContentAndStructureAndTheCopyIsSealed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "one", 0o644)
	write(t, root, "d/b.sh", "two", 0o755)
	write(t, root, ".git/config", "ignored", 0o644)
	if err := os.Symlink("a.txt", filepath.Join(root, "ln")); err != nil {
		t.Fatal(err)
	}
	// an absolute target on this OS ("/etc/passwd" is not absolute on Windows, which needs a drive)
	outside, err := filepath.Abs(filepath.Join(t.TempDir(), "outside.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	live, err := DigestTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Flags) != 1 || live.Flags[0] != "out -> "+outside {
		t.Fatalf("flags %v", live.Flags)
	}
	dst := filepath.Join(t.TempDir(), "snap")
	snap, err := Snapshot(root, dst)
	t.Cleanup(func() { RemoveTree(dst) })
	if err != nil || snap.Digest != live.Digest {
		t.Fatalf("copy digests differently: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Fatal("the .git directory is part of the subject")
	}
	if err := os.WriteFile(filepath.Join(dst, "a.txt"), []byte("x"), 0o600); err == nil {
		t.Fatal("a snapshot file is writable")
	}
	if runtime.GOOS != "windows" { // NTFS ignores a directory's mode bits: only the files are sealed there (see seal in subject.go)
		if err := os.WriteFile(filepath.Join(dst, "new"), []byte("x"), 0o600); err == nil {
			t.Fatal("a snapshot directory is writable")
		}
	}
	if got, _ := DigestTree(dst, ""); got.Digest != live.Digest {
		t.Fatal("the sealed snapshot digests differently from its source")
	}
	// content changes the digest; a time change does not
	write(t, root, "a.txt", "ONE", 0o644)
	if changed, _ := DigestTree(root, ""); changed.Digest == live.Digest {
		t.Fatal("a content change kept the digest")
	}
	write(t, root, "a.txt", "one", 0o644)
	os.Chtimes(filepath.Join(root, "a.txt"), live0(), live0())
	if same, _ := DigestTree(root, ""); same.Digest != live.Digest {
		t.Fatal("a time change moved the digest")
	}
	if runtime.GOOS == "windows" {
		return // NTFS has no executable bit: flipping it changes nothing, so it cannot be part of the digest there
	}
	os.Chmod(filepath.Join(root, "a.txt"), 0o755)
	if mode, _ := DigestTree(root, ""); mode.Digest == live.Digest {
		t.Fatal("the executable bit is not part of the digest")
	}
}

func TestSnapshotRefusesHardLinksDevicesAndLeavesNothing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a", "x", 0o644)
	if err := os.Link(filepath.Join(root, "a"), filepath.Join(root, "b")); err != nil {
		t.Skip("no hard links here")
	}
	dst := filepath.Join(t.TempDir(), "snap")
	if _, err := Snapshot(root, dst); !errors.Is(err, ErrSnapshotRefused) {
		t.Fatalf("a hard link was snapshotted: %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a refused snapshot left a directory behind")
	}
}

func TestDiffAndRemoveTree(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, a, "x", "1", 0o644)
	write(t, a, "y", "1", 0o644)
	write(t, b, "x", "2", 0o644)
	write(t, b, "z", "1", 0o644)
	ta, _ := DigestTree(a, "")
	tb, _ := DigestTree(b, "")
	if d := Diff(ta, tb, 10); len(d) != 3 || d[0] != "x" || d[1] != "y" || d[2] != "z" {
		t.Fatalf("%v", d)
	}
	if d := Diff(ta, tb, 2); len(d) != 2 {
		t.Fatalf("not bounded: %v", d)
	}
	dst := filepath.Join(t.TempDir(), "s")
	if _, err := Snapshot(a, dst); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dst); err == nil {
		t.Fatal("the sealed tree was not removed")
	}
}
