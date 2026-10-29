//go:build darwin

package main

import "syscall"

// statfsName is the statfs f_fstypename of p; a seam for tests.
var statfsName = func(p string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return "", err
	}
	b := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b), nil
}

func windowsLinkGuard(string) error { return nil }

// onNetworkFilesystem: p (already resolved) is on nfs, smbfs, afpfs or webdav.
func onNetworkFilesystem(p string) bool {
	n, err := statfsName(p)
	return err == nil && isDarwinNetworkFSName(n)
}
