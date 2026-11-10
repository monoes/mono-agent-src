//go:build !windows

package verifier

import (
	"os"
	"syscall"
)

// listenUnix makes a real listening unix socket at path (no net package: this library opens no connection). The returned
// function closes it and removes the file.
func listenUnix(path string) (func(), error) {
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	if err := syscall.Listen(fd, 1); err != nil {
		syscall.Close(fd)
		os.Remove(path)
		return nil, err
	}
	return func() { syscall.Close(fd); os.Remove(path) }, nil
}
