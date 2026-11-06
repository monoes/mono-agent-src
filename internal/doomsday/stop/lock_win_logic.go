package stop

import "errors"

// The Windows file lock, split so its decisions run on every OS: the real calls (lock_windows.go) sit behind
// winLockOS, and the mapping of "someone else holds it" to ErrSecondHost, and of everything else to a refusal, lives here.

var errNoUnlock = errors.New("stop: the lock call returned no way to release it")

// winLockOS takes an exclusive lock on a file (created if missing). contended is true when the lock is held by
// someone else and wait was false; unlock releases and closes. A non-nil err with contended false is any other failure.
type winLockOS interface {
	LockFile(path string, wait bool) (unlock func(), contended bool, err error)
}

// lockVia is flock on top of a winLockOS: a taken lock is ErrSecondHost, a failure is never a lock.
func lockVia(o winLockOS, path string, wait bool) (func(), error) {
	unlock, contended, err := o.LockFile(path, wait)
	switch {
	case contended:
		return nil, ErrSecondHost
	case err != nil:
		return nil, err
	case unlock == nil:
		return nil, errNoUnlock
	}
	return unlock, nil
}
