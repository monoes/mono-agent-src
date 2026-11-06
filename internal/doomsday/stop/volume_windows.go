//go:build windows

package stop

import "golang.org/x/sys/windows"

// NOT VERIFIED on a real Windows host (compiles with GOOS=windows go vet).
func volumeClass(dir string) (string, bool, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", false, err
	}
	var root [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumePathName(p, &root[0], uint32(len(root))); err != nil {
		return "", false, err
	}
	var fs [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))); err != nil {
		return "", false, err
	}
	remote := windows.GetDriveType(&root[0]) == windows.DRIVE_REMOTE
	return windows.UTF16ToString(fs[:]), remote, nil
}
