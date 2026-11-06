//go:build darwin

package stop

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// NOT VERIFIED on a real macOS host: this file compiles (GOOS=darwin go vet) and the decisions around it are tested
// through vnodeSys on Linux (witness_pin_vnodes.go has the guarantees and their limits); kqueue, O_EVTONLY and
// F_GETPATH have not run here, nor has the sequence been run against SQLite on macOS.

func newPinWatch(main string) (pinWatch, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(kq)
	w, err := newVnodeWatch(&kqueueSys{kq: kq, byFD: map[int]string{}}, main)
	if err != nil {
		return nil, err
	}
	return w, nil
}

type kqueueSys struct {
	mu   sync.Mutex
	kq   int
	byFD map[int]string
	shut bool
}

// Watch opens the vnode for events only (O_EVTONLY: no read access, and it does not keep a volume from unmounting)
// and registers NOTE_DELETE, NOTE_RENAME and NOTE_REVOKE, cleared when read.
func (k *kqueueSys) Watch(path string) (FileID, error) {
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return FileID{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return FileID{}, err
	}
	var ev unix.Kevent_t
	unix.SetKevent(&ev, fd, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_ENABLE|unix.EV_CLEAR)
	ev.Fflags = unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_REVOKE
	if _, err := unix.Kevent(k.kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		_ = unix.Close(fd)
		return FileID{}, err
	}
	k.mu.Lock()
	k.byFD[fd] = path
	k.mu.Unlock()
	return FileID{Dev: uint64(st.Dev), Ino: st.Ino}, nil //nolint:unconvert // width differs per arch
}

func (*kqueueSys) IDOf(path string) (FileID, error) { return statID(path) }

// Events reads what is queued without waiting. An error flag on an event, or a read error, is an error.
func (k *kqueueSys) Events() ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	buf := make([]unix.Kevent_t, 32)
	zero := unix.Timespec{}
	for {
		n, err := unix.Kevent(k.kq, nil, buf, &zero)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, ev := range buf[:n] {
			if ev.Flags&unix.EV_ERROR != 0 {
				return nil, fmt.Errorf("kevent error %d", ev.Data)
			}
			out = append(out, k.byFD[int(ev.Ident)])
		}
		if n < len(buf) {
			return out, nil
		}
	}
}

func (k *kqueueSys) Owns(fd int) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.byFD[fd]
	return fd == k.kq || ok
}

func (k *kqueueSys) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.shut {
		return
	}
	k.shut = true
	for fd := range k.byFD {
		_ = unix.Close(fd)
	}
	_ = unix.Close(k.kq)
}

// scanFDs lists the descriptors of this process through /dev/fd, each with its identity (fstat) and the path the
// kernel reports for it (F_GETPATH; it follows a rename, so a descriptor on a moved file shows the new name). A
// descriptor that disappears while listing is skipped; one that exists and cannot be identified fails closed, except
// descriptors that are not files (a socket, pipe or kqueue has no path).
func scanFDs() ([]fdEntry, error) {
	names, err := os.ReadDir("/dev/fd")
	if err != nil {
		return nil, fmt.Errorf("stop: cannot list the descriptors: %w", err)
	}
	var out []fdEntry
	for _, n := range names {
		fd, aerr := strconv.Atoi(n.Name())
		if aerr != nil {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			if errors.Is(err, unix.EBADF) { // closed since the listing (also the listing's own descriptor)
				continue
			}
			return nil, fmt.Errorf("stop: descriptor %d cannot be identified: %w", fd, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		var buf [unix.PathMax]byte
		link := ""
		if _, _, e := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))); e == 0 {
			for i, c := range buf {
				if c == 0 {
					link = string(buf[:i])
					break
				}
			}
		}
		out = append(out, fdEntry{fd: fd, link: link, id: FileID{Dev: uint64(st.Dev), Ino: st.Ino}}) //nolint:unconvert
	}
	return out, nil
}
