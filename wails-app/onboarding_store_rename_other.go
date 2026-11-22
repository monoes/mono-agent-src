//go:build !windows

package main

import "os"

// replaceGuiStateFile renames from over to. A POSIX rename replaces the
// target even while another handle has it open.
func replaceGuiStateFile(from, to string) error {
	return os.Rename(from, to)
}
