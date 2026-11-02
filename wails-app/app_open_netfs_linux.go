//go:build linux

package main

import "syscall"

// statfsType is the statfs f_type of p; a seam for tests.
var statfsType = func(p string) (uint32, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, err
	}
	return uint32(st.Type), nil
}

func windowsLinkGuard(string) error { return nil }

// onNetworkFilesystem: p (already resolved) is on a network filesystem by
// statfs magic number. Best effort: statfs reports the type of the mount that
// holds p, so an overlayfs with an NFS/network lower layer reports overlay
// and FUSE mounts report FUSE; neither is detected. It runs after the path
// was resolved, so it stops the use of a network result, not the contact. An
// error means "not known to be network".
func onNetworkFilesystem(p string) bool {
	t, err := statfsType(p)
	return err == nil && isLinuxNetworkFSMagic(t)
}
