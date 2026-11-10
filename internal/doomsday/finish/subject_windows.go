//go:build windows

package finish

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// NOT VERIFIED on a real Windows host beyond the snapshot tests that exercise it (subject_test.go).

// hardLinked reports whether the file has more than one link (NumberOfLinks of its handle). A file whose handle or
// information cannot be read counts as linked: the snapshot refuses it rather than copy something it could not look at.
func hardLinked(path string, _ fs.FileInfo) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	h, err := windows.CreateFile(p, 0x80, // FILE_READ_ATTRIBUTES
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return true
	}
	return info.NumberOfLinks > 1
}

// openRegular opens path and returns it only if the open handle is a regular file and the very file the walk saw (info).
func openRegular(path string, info fs.FileInfo) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(st, info) {
		f.Close()
		return nil, fmt.Errorf("changed after it was looked at, or is not a regular file")
	}
	return f, nil
}
