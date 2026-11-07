//go:build windows

package procbind

import (
	"errors"
	"net"
	"os"
	"path/filepath"
)

// readyProbes are what Ready asks of the machine; tests replace it.
var readyProbes = struct{ peerPID func() error }{peerPID: probePeerPID}

// Ready says whether the binding service can run on this machine: Unix-domain sockets exist and the kernel names the
// process at the other end of one (SIO_AF_UNIX_GETPEERPID). The answers are fixed texts: they name no path or key.
func Ready() error {
	if err := readyProbes.peerPID(); err != nil {
		return errors.New("this Windows cannot tell which process connected to a local socket (it needs the peer-pid query of Windows 10 version 1809 or later)")
	}
	return nil
}

// PeerPidfdSupported is false: Linux's pidfd does not exist here; the Windows service pins the peer with a process handle.
func PeerPidfdSupported() bool { return false }

// probePeerPID connects a socket to itself and asks for the peer: it must be this process.
func probePeerPID() error {
	dir, err := os.MkdirTemp("", "mbp")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "p.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer l.Close()
	type res struct {
		c   *net.UnixConn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := l.AcceptUnix(); ch <- res{c, err} }()
	cl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer cl.Close()
	r := <-ch
	if r.err != nil {
		return r.err
	}
	defer r.c.Close()
	pid, err := peerPID(r.c)
	if err != nil {
		return err
	}
	if int(pid) != os.Getpid() {
		return errors.New("the peer pid is not this process")
	}
	return nil
}
