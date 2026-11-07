//go:build darwin

package procbind

import (
	"errors"
	"net"
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
// (or a slow table) must not be able to make the service spend its time here. maxHolderFDs counts ALL descriptors of the peer
// as the first listing returns them (sockets or not); holderBudget is checked between descriptors, so one proc_info call that
// is itself slow is not interrupted. A peer over either bound is refused. A client
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

// checkLayout is the self-check on a table it reads through info: two connections made inside this process, as descriptor
// pairs. Each pair's ends must be each other's holder, and the other pair must NOT be (the negative control: offsets that
// read one field twice, or any field that is the same for every socket, would otherwise accept any holder of any connected
// socket).
func checkLayout(info func(fd int) (own, conn uint64, err error), a, b [2]int) error {
	bad := errors.New("the kernel's socket information has a layout this build does not know")
	var own [2][2]uint64
	for i, pair := range [2][2]int{a, b} {
		o0, c0, err0 := info(pair[0])
		o1, c1, err1 := info(pair[1])
		if err0 != nil || err1 != nil || o0 == 0 || o1 == 0 || o0 == c0 || o1 == c1 || c0 != o1 || c1 != o0 {
			return bad
		}
		own[i] = [2]uint64{o0, o1}
	}
	if own[0][0] == own[1][0] || own[0][0] == own[1][1] || own[0][1] == own[1][0] || own[0][1] == own[1][1] {
		return bad
	}
	far := time.Now().Add(time.Minute)
	o0, c0, _ := info(a[0])
	o1, c1, _ := info(a[1])
	// each end of a is found, among a's descriptors, as the holder of the other end ...
	if findHolder(a[:], info, o0, c0, far) != nil || findHolder(a[:], info, o1, c1, far) != nil {
		return bad
	}
	// ... and not among b's: b holds no end of a's connection
	if findHolder(b[:], info, o0, c0, far) == nil || findHolder(b[:], info, o1, c1, far) == nil {
		return bad
	}
	return nil
}

// layoutCheck proves, on this machine, that the offsets above read what they are taken to read (see checkLayout). A kernel
// whose structures moved fails this, and Ready then refuses instead of the service denying everything or matching garbage.
func layoutCheck() error {
	var fds [4]int
	for i := 0; i < 4; i += 2 {
		p, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			for _, fd := range fds[:i] {
				unix.Close(fd)
			}
			return errors.New("a socket pair could not be made")
		}
		fds[i], fds[i+1] = p[0], p[1]
	}
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}()
	self := unix.Getpid()
	return checkLayout(func(fd int) (uint64, uint64, error) { return fdSocketPCBs(self, fd) }, [2]int{fds[0], fds[1]}, [2]int{fds[2], fds[3]})
}
