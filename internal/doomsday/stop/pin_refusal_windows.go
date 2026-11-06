//go:build windows

package stop

import (
	"errors"

	"golang.org/x/sys/windows"
)

// pinRefusalExpected: the class of error Windows gives for a delete or rename of a file or directory that a handle
// without FILE_SHARE_DELETE holds. Anything else (not found, invalid name) is not the binding working.
func pinRefusalExpected(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
