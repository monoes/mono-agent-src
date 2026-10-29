//go:build linux

package procbind

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// pidfdHandle is a pidfd: the kernel keeps it naming one process incarnation.
type pidfdHandle struct {
	mu sync.Mutex
	fd int // -1 once closed
}

var errClosed = errors.New("procbind: the handle is closed")

// OpenPidfd pins a process by number. The caller must know the number names the process it means
// (the host opens a pidfd at fork, the service gets one from the connection); a bare number read
// from /proc is what the walk brackets with liveness checks.
func OpenPidfd(pid int) (Handle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("procbind: pidfd_open(%d): %w", pid, err)
	}
	return &pidfdHandle{fd: fd}, nil
}

// HandleFromPidfd adopts an already open pidfd (one the host got at fork, or SO_PEERPIDFD); the
// handle closes it.
func HandleFromPidfd(fd int) Handle { return &pidfdHandle{fd: fd} }

// Alive polls the pidfd: it becomes readable when the process exits, reaped or not. Anything
// the kernel will not say is not alive.
func (h *pidfdHandle) Alive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fd < 0 {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	for errors.Is(err, unix.EINTR) {
		n, err = unix.Poll(fds, 0)
	}
	return err == nil && n == 0
}

// Pid reads the process number from the pidfd's fdinfo ("Pid:" is -1 once the process is reaped).
func (h *pidfdHandle) Pid() (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fd < 0 {
		return 0, errClosed
	}
	b, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(h.fd))
	if err != nil {
		return 0, err
	}
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if v, ok := bytes.CutPrefix(line, []byte("Pid:")); ok {
			pid, err := strconv.Atoi(string(bytes.TrimSpace(v)))
			if err != nil || pid <= 0 {
				return 0, errors.New("procbind: the process is gone")
			}
			return pid, nil
		}
	}
	return 0, errors.New("procbind: no Pid in fdinfo")
}

// Close is idempotent: the descriptor number is forgotten, so a second call cannot close another
// file that was given the same number.
func (h *pidfdHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fd < 0 {
		return nil
	}
	fd := h.fd
	h.fd = -1
	return unix.Close(fd)
}

// LinuxProcs reads the process table of the pid namespace and procfs mount it runs in. A pidfd's
// number is in the reader's pid namespace, and /proc/<n> is indexed by the namespace of the procfs
// mount, so the two must be the same: NewLinuxProcs checks it, and the zero value reads nothing.
type LinuxProcs struct{ ok bool }

var errProcfs = errors.New("procbind: /proc is not this process's own procfs (another pid namespace or a mount over /proc)")

// NewLinuxProcs checks that /proc/self is this process: the mount in front of /proc belongs to
// the pid namespace this process is in. It fails closed otherwise.
func NewLinuxProcs() (LinuxProcs, error) {
	target, err := os.Readlink("/proc/self")
	if err != nil || target != strconv.Itoa(os.Getpid()) {
		return LinuxProcs{}, errProcfs
	}
	return LinuxProcs{ok: true}, nil
}

func (p LinuxProcs) Open(pid int) (Handle, error) {
	if !p.ok {
		return nil, errProcfs
	}
	return OpenPidfd(pid)
}

func (p LinuxProcs) Parent(h Handle) (int, error) {
	if !p.ok {
		return 0, errProcfs
	}
	pid, err := h.Pid()
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, err
	}
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if v, ok := bytes.CutPrefix(line, []byte("PPid:")); ok {
			return strconv.Atoi(string(bytes.TrimSpace(v)))
		}
	}
	return 0, errors.New("procbind: no PPid in status")
}

// Start is field 22 of /proc/<pid>/stat: clock ticks since boot at which the process started.
func (p LinuxProcs) Start(h Handle) (uint64, error) {
	if !p.ok {
		return 0, errProcfs
	}
	pid, err := h.Pid()
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// the command name (field 2) is in parentheses and may hold spaces: count from the last ')'
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, errors.New("procbind: malformed stat")
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 20 { // state is field 3; starttime is field 22, the 20th after the name
		return 0, errors.New("procbind: short stat")
	}
	return strconv.ParseUint(string(f[19]), 10, 64)
}
