//go:build windows

package stop

import (
	"path/filepath"
	"testing"
)

func TestSyncDirOnWindowsSucceedsOnADirectoryAndFailsOnAMissingOne(t *testing.T) {
	mustNoErr(t, OSFS{}.SyncDir(t.TempDir()))
	if err := (OSFS{}).SyncDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a missing directory synced")
	}
}
