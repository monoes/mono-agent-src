//go:build windows

package main

import "os"

// openChatFileNoFollow: Windows has no O_NOFOLLOW; the caller verifies the
// opened file against a fresh resolve of the path.
func openChatFileNoFollow(p string) (*os.File, error) { return os.Open(p) }
