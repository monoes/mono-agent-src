//go:build darwin

package procbind

import (
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// holdsConnection proves that the process pid holds the peer's end of the connection c: it has a unix socket open whose
// kernel socket control block is the one c is connected to (and which is connected back to c). The kernel's own tables
// say so (proc_info: the descriptors of a process and the socket information of each), nothing the peer sends does.
// It returns nil only on that proof; a process that cannot be read, holds no such descriptor, or any answer of a shape
// this code does not know is an error.

const (
	sysProcInfo        = 336 // proc_info(2)
	callPidInfo        = 2   // PROC_INFO_CALL_PIDINFO
	callPidFDInfo      = 3   // PROC_INFO_CALL_PIDFDINFO
	flavorListFDs      = 1   // PROC_PIDLISTFDS
	flavorFDSocketInfo = 3   // PROC_PIDFDSOCKETINFO
	fdTypeSocket       = 2   // PROX_FDTYPE_SOCKET
	fdInfoSize         = 8   // sizeof(struct proc_fdinfo)
	fileInfoSize       = 24  // sizeof(struct proc_fileinfo), before the socket_info of a socket_fdinfo
	socketFDInfoSize   = 792 // sizeof(struct socket_fdinfo)
	maxFDListBytes     = 1 << 20
	afUnix             = 1
)

// Offsets inside the socket_info that follows the proc_fileinfo: soi_pcb, soi_family, and the unix socket's unsi_conn_pcb.
const (
	offPCB     = fileInfoSize + 144
	offFamily  = fileInfoSize + 160
	offConnPCB = fileInfoSize + 248
)

func procInfoCall(call, pid, flavor, arg int, buf []byte) (int, error) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	n, _, errno := unix.Syscall6(sysProcInfo, uintptr(call), uintptr(pid), uintptr(flavor), uintptr(arg), uintptr(p), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// socketPCBs is a unix socket's own control block and the one it is connected to, from the answer of PROC_PIDFDSOCKETINFO.
func socketPCBs(buf []byte, n int) (own, conn uint64, err error) {
	if n != socketFDInfoSize || len(buf) < socketFDInfoSize {
		return 0, 0, errors.New("procbind: short socket information")
	}
	if *(*int32)(unsafe.Pointer(&buf[offFamily])) != afUnix {
		return 0, 0, errors.New("procbind: not a unix socket")
	}
	own, conn = *(*uint64)(unsafe.Pointer(&buf[offPCB])), *(*uint64)(unsafe.Pointer(&buf[offConnPCB]))
	if own == 0 || conn == 0 {
		return 0, 0, errors.New("procbind: the socket is not connected")
	}
	return own, conn, nil
}

func fdSocketPCBs(pid, fd int) (own, conn uint64, err error) {
	var buf [socketFDInfoSize]byte
	n, err := procInfoCall(callPidFDInfo, pid, flavorFDSocketInfo, fd, buf[:])
	if err != nil {
		return 0, 0, err
	}
	return socketPCBs(buf[:], n)
}

// socketFDs lists the descriptors of pid that are sockets.
func socketFDs(pid int) ([]int, error) {
	n, err := procInfoCall(callPidInfo, pid, flavorListFDs, 0, nil)
	if err != nil || n <= 0 || n > maxFDListBytes {
		return nil, errors.New("procbind: the descriptors of the process cannot be listed")
	}
	buf := make([]byte, n+16*fdInfoSize) // room for descriptors opened since the size was asked
	n, err = procInfoCall(callPidInfo, pid, flavorListFDs, 0, buf)
	if err != nil {
		return nil, errors.New("procbind: the descriptors of the process cannot be listed")
	}
	var fds []int
	for i := 0; i+fdInfoSize <= n; i += fdInfoSize {
		if *(*uint32)(unsafe.Pointer(&buf[i+4])) == fdTypeSocket {
			fds = append(fds, int(*(*int32)(unsafe.Pointer(&buf[i]))))
		}
	}
	return fds, nil
}

func holdsConnection(c *net.UnixConn, pid int) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var own, peerPCB uint64
	var ierr error
	if err := rc.Control(func(fd uintptr) { own, peerPCB, ierr = fdSocketPCBs(unix.Getpid(), int(fd)) }); err != nil {
		return err
	}
	if ierr != nil {
		return ierr
	}
	fds, err := socketFDs(pid)
	if err != nil {
		return err
	}
	for _, fd := range fds {
		theirs, theirPeer, err := fdSocketPCBs(pid, fd)
		if err == nil && theirs == peerPCB && theirPeer == own {
			return nil
		}
	}
	return errors.New("procbind: the process holds no descriptor of this connection")
}
