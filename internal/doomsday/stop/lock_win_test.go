package stop

import (
	"errors"
	"testing"
)

// fakeWinLock models LockFileEx on one file: a second exclusive lock on a path another handle holds fails at once
// (ERROR_LOCK_VIOLATION) when wait is false, whoever asks, in this process or another.
type fakeWinLock struct {
	held    map[string]bool
	failErr error
}

func (f *fakeWinLock) LockFile(path string, wait bool) (func(), bool, error) {
	if f.failErr != nil {
		return nil, false, f.failErr
	}
	if f.held[path] {
		return nil, true, nil
	}
	f.held[path] = true
	return func() { delete(f.held, path) }, false, nil
}

func TestWinLockSecondHolderIsErrSecondHostAndReleaseFrees(t *testing.T) {
	f := &fakeWinLock{held: map[string]bool{}}
	unlock, err := lockVia(f, `C:\run\host.lock`, false)
	mustNoErr(t, err)
	if _, err := lockVia(f, `C:\run\host.lock`, false); !errors.Is(err, ErrSecondHost) {
		t.Fatalf("a taken lock must be ErrSecondHost: %v", err)
	}
	if _, err := lockVia(f, `C:\run\other.lock`, false); err != nil {
		t.Fatalf("another file is independent: %v", err)
	}
	unlock()
	u2, err := lockVia(f, `C:\run\host.lock`, false)
	mustNoErr(t, err)
	u2()
}

func TestWinLockFailureIsNeverALock(t *testing.T) {
	boom := errors.New("access denied")
	u, err := lockVia(&fakeWinLock{held: map[string]bool{}, failErr: boom}, "x", false)
	if u != nil || !errors.Is(err, boom) || errors.Is(err, ErrSecondHost) {
		t.Fatalf("a failed lock call must return its error and no unlock: %v %v", u != nil, err)
	}
	// a lock call that reports success but gives no way to release is not a lock
	if u, err := lockVia(noUnlockOS{}, "x", false); u != nil || err == nil {
		t.Fatalf("success without an unlock passed: %v", err)
	}
}

type noUnlockOS struct{}

func (noUnlockOS) LockFile(string, bool) (func(), bool, error) { return nil, false, nil }
