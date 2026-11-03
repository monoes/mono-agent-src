//go:build windows

package coderhost

import "os"

// ownedByUs: ownership is not read on Windows, where process ancestry is unsupported and the registry is never used.
func ownedByUs(os.FileInfo) bool { return true }
