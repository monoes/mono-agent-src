//go:build !unix && !windows

package storage

import "os"

// No cross-process lock on these platforms: none is shipped (unix uses flock,
// windows uses LockFileEx), so single-process migration is assumed.
func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }

func tryLockFile(*os.File) (bool, error) { return true, nil }
