//go:build !darwin && !windows && !linux

package chromium

import "os"

// No candidates on other systems: every browser is "not installed", so the
// package still builds everywhere.
func candidatePaths(string) []string { return nil }

func launchArgs(path string) (string, []string) { return path, nil }

func installed(fi os.FileInfo) bool { return fi.Mode().IsRegular() }
