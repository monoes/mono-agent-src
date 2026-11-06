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

// The host lock is held for the host's life. A same-user process that unlinks host.lock afterwards lets a second host
// lock a fresh file; the host finds that out at its next look and fails closed.
func TestHeldHostLockIsVerifiedAtEachLookAndFailsClosedWhenTheFileIsGone(t *testing.T) {
	r := RunDir{Dir: t.TempDir()}
	release, err := AcquireHost(r)
	mustNoErr(t, err)
	defer release()
	mustNoErr(t, VerifyHostLock(r))
	mustNoErr(t, os.Remove(filepath.Join(r.Dir, "host.lock")))
	if err := VerifyHostLock(r); !errors.Is(err, ErrHostLockLost) {
		t.Fatalf("an unlinked host lock was not detected: %v", err)
	}
	// replaced by a new file at the same name: also lost
	mustNoErr(t, os.WriteFile(filepath.Join(r.Dir, "host.lock"), nil, 0o600))
	if err := VerifyHostLock(r); !errors.Is(err, ErrHostLockLost) {
		t.Fatalf("a replaced host lock was not detected: %v", err)
	}
}

func TestVerifyHostLockIsQuietWhenThisProcessHoldsNone(t *testing.T) {
	mustNoErr(t, VerifyHostLock(RunDir{Dir: t.TempDir()}))
}

func TestVerifyHostLockForgetsAReleasedLock(t *testing.T) {
	r := RunDir{Dir: t.TempDir()}
	release, err := AcquireHost(r)
	mustNoErr(t, err)
	release()
	mustNoErr(t, os.Remove(filepath.Join(r.Dir, "host.lock")))
	mustNoErr(t, VerifyHostLock(r))
}

// Every chained write takes the witness lock; it refuses when the host lock this process holds is no longer the file at
// its name.
func TestWitnessLockRefusesWhenTheHostLockWasUnlinked(t *testing.T) {
	r := RunDir{Dir: t.TempDir()}
	release, err := AcquireHost(r)
	mustNoErr(t, err)
	defer release()
	w := Witness{Dir: r}
	unlock, err := w.lockBounded()
	mustNoErr(t, err)
	unlock()
	mustNoErr(t, os.Remove(filepath.Join(r.Dir, "host.lock")))
	if u, err := w.lockBounded(); !errors.Is(err, ErrHostLockLost) {
		if u != nil {
			u()
		}
		t.Fatalf("the witness lock was granted to a host whose lock is gone: %v", err)
	}
	if u, err := w.LockWitness(); !errors.Is(err, ErrHostLockLost) {
		if u != nil {
			u()
		}
		t.Fatalf("LockWitness was granted to a host whose lock is gone: %v", err)
	}
}
