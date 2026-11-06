//go:build darwin

package stop

import (
	"fmt"
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

// scanFDs lists the descriptors of this process through /dev/fd (names only), each with its identity (fstat) and the path
// the kernel reports for it (F_GETPATH; it follows a rename, so a descriptor on a moved file shows the new name).
func scanFDs() ([]fdEntry, error) {
	return scanFDsVia(fdProbe{
		Names: func() ([]string, error) { return readFDNames("/dev/fd") },
		Stat: func(fd int) (FileID, bool, error) {
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				return FileID{}, false, err
			}
			file := st.Mode&unix.S_IFMT == unix.S_IFREG || st.Mode&unix.S_IFMT == unix.S_IFDIR
			return FileID{Dev: uint64(st.Dev), Ino: st.Ino}, file, nil //nolint:unconvert
		},
		Path: func(fd int) string {
			var buf [unix.PathMax]byte
			if _, _, e := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
				return ""
			}
			for i, c := range buf {
				if c == 0 {
					return string(buf[:i])
				}
			}
			return ""
		},
	})
}
