//go:build windows

package finish

import "io/fs"

// The mode does not start on Windows (no containment), so no subject is ever made there; a file is never reported
// as a hard link, which the refusing platform makes moot.
func hardLinked(fs.FileInfo) bool { return false }
