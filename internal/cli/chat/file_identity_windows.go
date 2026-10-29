//go:build windows

package chat

import "os"

// fileIdentity: no identity is read here, so no attachment copy is ever removed on Windows.
func fileIdentity(os.FileInfo) (dev, ino uint64, ok bool) { return 0, 0, false }
