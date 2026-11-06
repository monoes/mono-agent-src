//go:build linux || darwin

package stop

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// fdProbe is what the macOS descriptor scan needs of the OS, so its decisions run (and are tested) on Linux too.
type fdProbe struct {
	// Names lists the descriptor numbers as names, WITHOUT looking at what each is: os.ReadDir lstat's every entry, and a
	// kqueue descriptor (the watch's own) is not a file, so the lstat fails with EBADF and the whole listing with it.
	Names func() ([]string, error)
	// Stat gives the identity of a descriptor and whether it is a regular file or a directory (anything else is skipped).
	Stat func(fd int) (id FileID, file bool, err error)
	// Path is the path the kernel reports for a descriptor ("" when it reports none).
	Path func(fd int) string
}

// readFDNames lists dir (/dev/fd) by name only.
func readFDNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// scanFDsVia lists the descriptors of this process, each with its identity and path. A descriptor that disappears while
// listing (EBADF, also the listing's own) is skipped; one that exists and cannot be identified fails closed, except
// descriptors that are not files (a socket, a pipe, a kqueue has no path).
func scanFDsVia(p fdProbe) ([]fdEntry, error) {
	names, err := p.Names()
	if err != nil {
		return nil, fmt.Errorf("stop: cannot list the descriptors: %w", err)
	}
	var out []fdEntry
	for _, n := range names {
		fd, aerr := strconv.Atoi(n)
		if aerr != nil {
			continue
		}
		id, file, err := p.Stat(fd)
		if err != nil {
			if errors.Is(err, unix.EBADF) {
				continue
			}
			return nil, fmt.Errorf("stop: descriptor %d cannot be identified: %w", fd, err)
		}
		if !file {
			continue
		}
		out = append(out, fdEntry{fd: fd, link: p.Path(fd), id: id})
	}
	return out, nil
}
