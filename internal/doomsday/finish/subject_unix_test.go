//go:build !windows

package finish

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSnapshotRefusesAPipe(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a", "x", 0o644)
	if err := syscall.Mkfifo(filepath.Join(root, "p"), 0o600); err != nil {
		t.Skip("no fifo")
	}
	dst := filepath.Join(t.TempDir(), "snap")
	if _, err := Snapshot(root, dst); !errors.Is(err, ErrSnapshotRefused) {
		t.Fatalf("a pipe was snapshotted: %v", err)
	}
}

func TestOpenRegularRefusesALinkAPipeAndAnotherFileAndNeverWaits(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a", "x", 0o644)
	write(t, root, "b", "y", 0o644)
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Skip("no symlinks")
	}
	if err := syscall.Mkfifo(filepath.Join(root, "p"), 0o600); err != nil {
		t.Skip("no fifo")
	}
	infoA, _ := os.Lstat(filepath.Join(root, "a"))
	infoB, _ := os.Lstat(filepath.Join(root, "b"))
	if f, err := openRegular(filepath.Join(root, "a"), infoA); err != nil {
		t.Fatalf("a regular file the walk saw: %v", err)
	} else {
		f.Close()
	}
	for name, c := range map[string]struct {
		path string
		info os.FileInfo
	}{
		"a link where the walk saw the file": {filepath.Join(root, "link"), infoA},
		"another file than the walk saw":     {filepath.Join(root, "b"), infoA},
		"a file where the walk saw another":  {filepath.Join(root, "a"), infoB},
		"a pipe where the walk saw a file":   {filepath.Join(root, "p"), infoA},
	} {
		done := make(chan error, 1)
		go func() {
			f, err := openRegular(c.path, c.info)
			if f != nil {
				f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s was opened", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the open waited", name)
		}
	}
}
