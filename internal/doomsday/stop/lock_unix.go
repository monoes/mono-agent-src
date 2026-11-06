//go:build linux || darwin

package stop

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func flock(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrSecondHost
		}
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}
