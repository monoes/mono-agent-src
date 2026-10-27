//go:build windows

package workflow

// syncDir does nothing on Windows: a directory handle cannot be flushed there (FlushFileBuffers
// is refused), and a rename within a volume is already durable once it returns.
func syncDir(string) error { return nil }
