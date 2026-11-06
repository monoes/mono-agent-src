//go:build linux

package stop

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func newPinWatch(main string) (pinWatch, error) {
	w, err := newNSWatch(main)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// adopt: the inotify watch covers the names from before the first stat, so nothing is added.
func (*nsWatch) adopt(string, FileID) error { return nil }

// scanFDs lists the readable descriptors of this process. A descriptor that disappears while listing is
// skipped; one that exists and cannot be read fails closed.
func scanFDs() ([]fdEntry, error) {
	names, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, fmt.Errorf("stop: cannot list the descriptors: %w", err)
	}
	var out []fdEntry
	for _, n := range names {
		p := "/proc/self/fd/" + n.Name()
		link, lerr := os.Readlink(p)
		fi, serr := os.Stat(p)
		if errors.Is(lerr, os.ErrNotExist) || errors.Is(serr, os.ErrNotExist) {
			continue
		}
		if lerr != nil || serr != nil {
			if strings.HasPrefix(link, "socket:") || strings.HasPrefix(link, "anon_inode:") || strings.HasPrefix(link, "pipe:") {
				continue
			}
			return nil, fmt.Errorf("stop: descriptor %s cannot be read, so it cannot be identified", n.Name())
		}
		id, ok := idOfInfo(fi)
		if !ok {
			continue
		}
		out = append(out, fdEntry{link: link, id: id})
	}
	return out, nil
}

// ----- the namespace watch -----

type watchSpec struct {
	names map[string]bool
	self  bool
}

type nsWatch struct {
	fd   int
	wds  map[int32]watchSpec
	buf  []byte
	done bool
}

// newNSWatch watches the database's directory (the four names, and the directory itself) and, for every
// ancestor, the entry that leads to the database.
func newNSWatch(main string) (*nsWatch, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &nsWatch{fd: fd, wds: map[int32]watchSpec{}, buf: make([]byte, 64<<10)}
	base := filepath.Base(main)
	add := func(path string, mask uint32, spec watchSpec) error {
		wd, err := syscall.InotifyAddWatch(fd, path, mask)
		if err != nil {
			return err
		}
		w.wds[int32(wd)] = spec
		return nil
	}
	const ns = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO
	dir := filepath.Dir(main)
	if err := add(dir, ns|syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF,
		watchSpec{names: map[string]bool{base: true, base + "-wal": true, base + "-shm": true, base + "-journal": true}, self: true}); err != nil {
		w.close()
		return nil, err
	}
	for child := dir; filepath.Dir(child) != child; child = filepath.Dir(child) {
		if err := add(filepath.Dir(child), ns, watchSpec{names: map[string]bool{filepath.Base(child): true}}); err != nil {
			w.close()
			return nil, err
		}
	}
	return w, nil
}

func (w *nsWatch) close() {
	if !w.done {
		w.done = true
		_ = syscall.Close(w.fd)
	}
}

// drain reads the queue. During the pin (allowCreate) the creation of the -wal and -shm names is expected.
func (w *nsWatch) drain(allowCreate bool) error {
	if w.done {
		return nil
	}
	for {
		n, err := syscall.Read(w.fd, w.buf)
		if err == syscall.EAGAIN || (err == nil && n == 0) {
			return nil
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: the event queue cannot be read: %v", ErrPinSwapped, err)
		}
		if verr := w.scanEvents(w.buf[:n], allowCreate); verr != nil {
			return verr
		}
	}
}

func (w *nsWatch) scanEvents(b []byte, allowCreate bool) error {
	for len(b) >= syscall.SizeofInotifyEvent {
		// Decoded field by field in the host's byte order (the kernel's struct inotify_event): a cast to
		// syscall.InotifyEvent reads past a buffer that ends with an event, because Go pads the struct's zero-size Name.
		ev := struct {
			Wd        int32
			Mask, Len uint32
		}{int32(binary.NativeEndian.Uint32(b[0:])), binary.NativeEndian.Uint32(b[4:]), binary.NativeEndian.Uint32(b[12:])}
		size := syscall.SizeofInotifyEvent + int(ev.Len)
		if size > len(b) {
			break
		}
		name := strings.TrimRight(string(b[syscall.SizeofInotifyEvent:size]), "\x00")
		b = b[size:]
		if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
			return fmt.Errorf("%w: the event queue overflowed", ErrPinSwapped)
		}
		spec, known := w.wds[ev.Wd]
		if !known {
			continue
		}
		if ev.Mask&(syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF|syscall.IN_IGNORED) != 0 {
			return fmt.Errorf("%w: the database directory itself was moved or removed", ErrPinSwapped)
		}
		if !spec.names[name] {
			continue
		}
		if allowCreate && ev.Mask&syscall.IN_CREATE != 0 && (strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm")) {
			continue
		}
		return fmt.Errorf("%w: namespace event 0x%x on %s", ErrPinSwapped, ev.Mask, name)
	}
	return nil
}
