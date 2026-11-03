//go:build !windows

package finish

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
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
