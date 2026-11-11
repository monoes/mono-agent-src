//go:build linux

package verifier

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// plantPty makes a pseudo-terminal pair of the host's own and returns the slave's path (never a terminal of the person's); the
// function closes it.
func plantPty() (string, func(), error) {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return "", nil, err
	}
	fd := int(f.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		f.Close()
		return "", nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		f.Close()
		return "", nil, err
	}
	return fmt.Sprintf("/dev/pts/%d", n), func() { f.Close() }, nil
}
