//go:build windows

package stop

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NOT VERIFIED on a real Windows host: this file compiles (GOOS=windows go vet) and the decisions around it are
// tested through handleFS on Linux (witness_pin_handles.go has the guarantees and their limits); CreateFile,
// GetFileInformationByHandle and GetFileInformationByHandleEx have not run here, nor has SQLite (modernc) been run
// against the held handles.

const (
	fileReadData       = 0x0001 // FILE_READ_DATA, also FILE_LIST_DIRECTORY for a directory
	fileReadAttributes = 0x0080
)

var errNoWindowsID = errors.New("no file identity")

func newPinWatch(main string) (pinWatch, error) {
	w, err := newHandleWatch(winHandleFS{}, main)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func statID(path string) (FileID, error) { return winHandleFS{}.IDOf(path) }

// checkHolders: SQLite's own handles cannot be listed on Windows; what binds the connection to the files is the share
// mode of the handles held by the watch (checked by drain), not an inventory of descriptors.
func (*Pinned) checkHolders() error { return nil }

type winHandleFS struct{}

// OpenHeld opens with read access (data for a file, listing for a directory) and a share mode of READ|WRITE without
// DELETE, so no other open may ask for delete access while it lives. The handle is not inheritable (nil security
// attributes). FILE_FLAG_BACKUP_SEMANTICS is what lets a directory be opened.
func (winHandleFS) OpenHeld(path string, dir bool) (heldFile, error) {
	h, err := open(path, fileReadData|fileReadAttributes, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		return nil, err
	}
	return winHeld(h), nil
}

// IDOf opens afresh with attribute access only and the full share mode, so the look itself never blocks anyone.
func (winHandleFS) IDOf(path string) (FileID, error) {
	h, err := open(path, fileReadAttributes, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return FileID{}, err
	}
	defer windows.CloseHandle(h)
	return winHeld(h).ID()
}

func open(path string, access, share uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

type winHeld windows.Handle

// ID is the volume serial number and the 64-bit file index of the handle's file.
func (h winHeld) ID() (FileID, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(h), &info); err != nil {
		return FileID{}, err
	}
	idx := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	if info.VolumeSerialNumber == 0 && idx == 0 {
		return FileID{}, errNoWindowsID
	}
	return FileID{Dev: uint64(info.VolumeSerialNumber), Ino: idx}, nil
}

// fileStandardInfo is FILE_STANDARD_INFO.
type fileStandardInfo struct {
	AllocationSize, EndOfFile int64
	NumberOfLinks             uint32
	DeletePending, Directory  byte
}

func (h winHeld) DeletePending() (bool, error) {
	var info fileStandardInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(h), windows.FileStandardInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return false, err
	}
	return info.DeletePending != 0, nil
}

func (h winHeld) Close() error { return windows.CloseHandle(windows.Handle(h)) }
