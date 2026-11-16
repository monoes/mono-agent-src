//go:build windows

package main

// cliOverrideTrusted: Windows has no uid/mode bits to check, so any existing
// file is accepted.
func cliOverrideTrusted(p string) bool { return fileExists(p) }
