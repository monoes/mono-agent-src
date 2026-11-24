package browserctl

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// stateMu is the in-process half of the session-state lock; the file lock
// (sessions/.lock) is the half other processes see. Two browser commands of
// one context are two processes, so the file lock is what stops them losing
// each other's updates.
var stateMu sync.Mutex

// lockStateDir takes the exclusive session-state lock for dir and returns the
// function that releases it. Not re-entrant.
func lockStateDir(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stateMu.Lock()
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		stateMu.Unlock()
		return nil, fmt.Errorf("open session-state lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		stateMu.Unlock()
		return nil, fmt.Errorf("take session-state lock: %w", err)
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
		stateMu.Unlock()
	}, nil
}
