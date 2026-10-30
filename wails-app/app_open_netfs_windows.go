//go:build windows

package main

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

var procGetDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

// windowsLinkGuard: see walkChatLinks.
func windowsLinkGuard(p string) error { return walkChatLinks(p, chatLstat, chatReadlink) }

// onNetworkFilesystem: the drive of p is a network (mapped) drive.
// GetDriveTypeW is called with the volume root and reads no path. Best effort:
// the drive type of the RESOLVED path, so it stops the use of a network result,
// not the contact; walkChatLinks is what tries to prevent contact.
func onNetworkFilesystem(p string) bool {
	v := filepath.VolumeName(p)
	if len(v) != 2 || v[1] != ':' {
		return false
	}
	root, err := syscall.UTF16PtrFromString(v + `\`)
	if err != nil {
		return false
	}
	t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(root)))
	return isRemoteDriveType(uint32(t))
}
