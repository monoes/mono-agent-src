//go:build !linux && !darwin && !windows

package main

func windowsLinkGuard(string) error { return nil }

// onNetworkFilesystem: no probe on this OS.
func onNetworkFilesystem(string) bool { return false }
