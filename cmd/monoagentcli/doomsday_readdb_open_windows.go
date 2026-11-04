//go:build doomsday && windows

package main

import "os"

// openWitnessNoFollow: Windows has no O_NOFOLLOW; the caller checks the path with Lstat first and the opened file
// with Fstat after.
func openWitnessNoFollow(p string) (*os.File, error) { return os.Open(p) }
