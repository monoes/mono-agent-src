//go:build windows

package stop

import "os"

// ownerOf names the owner of a file for an error message.
func ownerOf(os.FileInfo) string { return "an unknown user" }

// fileOwnedByMe: Windows has no uid in a file's metadata here; the probe directories are the user's own, and the name,
// the type (directory or socket) and the age still have to match.
var fileOwnedByMe = func(os.FileInfo) bool { return true }
