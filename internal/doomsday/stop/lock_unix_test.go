//go:build linux || darwin

package stop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A lock file that was unlinked (and a new one created at its name) between the open and the lock locks nothing anyone
// else will see: a second host would lock the new inode. The lock is only held when the path still leads to the file
// that is locked.
func TestFlockOnAnUnlinkedLockFileIsRetriedOnTheNewFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "host.lock")
	n := 0
	afterFlock = func(path string) {
		if n == 0 {
			_ = os.Remove(path)
		}
		n++
	}
	t.Cleanup(func() { afterFlock = nil })
	unlock, err := flock(p, false)
	mustNoErr(t, err)
	defer unlock()
	if n != 2 {
		t.Fatalf("attempts: %d", n)
	}
	// the lock that is held is on the file now at the path: a second holder is refused
	afterFlock = nil
	if _, err := flock(p, false); !errors.Is(err, ErrSecondHost) {
		t.Fatalf("the lock that was returned is not on the file at the path: %v", err)
	}
}

func TestFlockGivesUpWhenTheFileKeepsBeingReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "host.lock")
	afterFlock = func(path string) { _ = os.Remove(path) }
	t.Cleanup(func() { afterFlock = nil })
	if unlock, err := flock(p, false); err == nil {
		unlock()
		t.Fatal("a lock on a file nobody can reach was reported as held")
	}
}
