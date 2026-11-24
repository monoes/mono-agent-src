//go:build !unix && !windows

package browserctl

import "os"

// No cross-process lock on these platforms; the process-local mutex remains.
func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }
