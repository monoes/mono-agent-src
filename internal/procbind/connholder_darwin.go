//go:build darwin

package procbind

import (
	"errors"
	"net"
	"os"
	"time"
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
	if err != nil || n <= 0 || n > maxHolderFDs*fdInfoSize {
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

// maxHolderFDs and holderBudget bound the proof: one proc_info call per descriptor, so a process with a huge descriptor table
// (or a slow table) must not be able to make the service spend its time here. A peer over either bound is refused. A client
// that holds the connection has a handful of descriptors.
var (
	maxHolderFDs = 4096
	holderBudget = 500 * time.Millisecond
)

var errHolderBudget = errors.New("procbind: the peer's descriptors could not be examined within the budget")

// findHolder says whether one of fds is the peer's end of the connection whose own control block is own and whose peer's is
// peerPCB: its control block is peerPCB and it is connected to own. info reads a descriptor's pair; a descriptor that cannot
// be read is skipped. It gives up at the deadline.
func findHolder(fds []int, info func(fd int) (own, conn uint64, err error), own, peerPCB uint64, deadline time.Time) error {
	for _, fd := range fds {
		if time.Now().After(deadline) {
			return errHolderBudget
		}
		theirs, theirPeer, err := info(fd)
		if err == nil && theirs == peerPCB && theirPeer == own {
			return nil
		}
	}
	return errors.New("procbind: the process holds no descriptor of this connection")
}

// holderProof is holdsConnection; a seam for tests.
var holderProof = holdsConnection

func holdsConnection(c *net.UnixConn, pid int) error {
	deadline := time.Now().Add(holderBudget)
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
	return findHolder(fds, func(fd int) (uint64, uint64, error) { return fdSocketPCBs(pid, fd) }, own, peerPCB, deadline)
}

// layoutCheck proves, on this machine, that the offsets above read what they are taken to read: two ends of a connection made
// inside this process must each be found as the holder of the other. A kernel whose structures moved fails this, and Ready
// then refuses instead of the service denying everything or matching garbage.
func layoutCheck() error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return errors.New("a socket pair could not be made")
	}
	var conns []*net.UnixConn
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			return errors.New("a socket pair could not be used")
		}
		defer c.Close()
		uc, ok := c.(*net.UnixConn)
		if !ok {
			return errors.New("a socket pair is not a unix connection")
		}
		conns = append(conns, uc)
	}
	for _, c := range conns {
		if err := holdsConnection(c, unix.Getpid()); err != nil {
			return errors.New("the kernel's socket information has a layout this build does not know")
		}
	}
	return nil
}
