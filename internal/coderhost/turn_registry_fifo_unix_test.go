//go:build !windows

package coderhost

import (
	"syscall"
	"testing"
)

func fifoSupported(t *testing.T) {}

func makeFifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot make a FIFO here: %v", err)
	}
}
