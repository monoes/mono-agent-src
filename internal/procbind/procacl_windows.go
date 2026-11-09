//go:build windows

package procbind

import (
	"syscall"
	"unsafe"
)

// WorkerAttributes (stub)
func WorkerAttributes() (proc, thread *syscall.SecurityAttributes, err error) {
	return &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{}))}, &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{}))}, nil
}

// LockSelf (stub)
func LockSelf() error { return nil }
