//go:build !windows

package workflow

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// maxWorkflowFileBytes bounds what is read from a workflow file.
const maxWorkflowFileBytes = 16 << 20

// readRegularFile reads a bounded regular file without ever blocking on a special one: the file is
// opened non-blocking and without following a final symlink, so a named pipe with no writer opens
// at once and a link is refused, and what is read is what the descriptor says it is.
func readRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxWorkflowFileBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrFileTooLarge, path, maxWorkflowFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWorkflowFileBytes+1))
	if err == nil && len(data) > maxWorkflowFileBytes {
		err = fmt.Errorf("%w: %s grew past %d bytes", ErrFileTooLarge, path, maxWorkflowFileBytes)
	}
	return data, err
}
