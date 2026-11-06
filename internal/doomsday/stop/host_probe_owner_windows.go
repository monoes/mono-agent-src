//go:build windows

package stop

import "os"

// fileOwnedByMe: Windows has no uid in a file's metadata here; the probe directories are the user's own, and the name,
// the type (directory or socket) and the age still have to match.
var fileOwnedByMe = func(os.FileInfo) bool { return true }
