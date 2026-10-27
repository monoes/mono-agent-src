//go:build windows

package workflow

import (
	"fmt"
	"io"
	"os"
)

const maxWorkflowFileBytes = 16 << 20

// readRegularFile reads a bounded regular file: the handle is opened, checked, and read with a cap,
// so what is read is what the handle says it is. Links are refused by a prior Lstat.
func readRegularFile(path string) ([]byte, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
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
