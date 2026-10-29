//go:build linux

package procbind

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Registry is the service's view of active registrations: the worker a token is bound to, as a
// held handle, only while the registration is active (never rebuilt from a database).
type Registry interface {
	Lookup(tokenID, profile, scope string) (worker Handle, ok bool)
}

// Server answers verification requests on a unix socket. It learns the caller from the kernel
// (SO_PEERCRED and a pidfd from SO_PEERPIDFD), never from the request, and walks the host's
// process table from that pidfd to the registered worker.
type Server struct {
	Profile    string
	Generation string
	Key        []byte // the answer key (K_ans)
	Registry   Registry
	Procs      Procs
	UID        int           // the only peer uid served
	Timeout    time.Duration // per connection; 5 s by default
	MaxConns   int           // concurrent connections; 32 by default
	// Logf receives the reason of every refusal. It is for the operator: reasons are never sent.
	Logf func(format string, args ...any)

	// RequestTimeout is how long the service waits for the request after its hello; 1 s by default.
	RequestTimeout time.Duration
	// Rate is the connections accepted per second (and the burst); 100 by default. An excess
	// connection is closed without a hello.
	Rate int

	once   sync.Once
	sem    chan struct{}
	bucket chan struct{}
	// peerOf reads the other end's credentials; tests replace it to stand in for a peer the
	// service cannot see. It is the real kernel lookup otherwise.
	peerOf func(*net.UnixConn) (unix.Ucred, int, error)
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// PeerPidfdSupported says whether this kernel gives a pidfd for a connected unix socket
// (SO_PEERPIDFD, Linux 6.5). Without it the feature is unavailable, not degraded.
func PeerPidfdSupported() bool {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	fd, err := unix.GetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return false
	}
	_ = unix.Close(fd)
	return true
}

// unixAcceptor is what Serve needs of a listener; a *net.UnixListener is one.
type unixAcceptor interface {
	AcceptUnix() (*net.UnixConn, error)
}

// Serve accepts connections until the listener is closed; each is handled on its own goroutine, up
// to MaxConns at a time, and an excess connection is closed without an answer.
func (s *Server) Serve(l unixAcceptor) error {
	s.init()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // refill the rate bucket
		t := time.NewTicker(time.Second / time.Duration(cap(s.bucket)))
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				select {
				case s.bucket <- struct{}{}:
				default:
				}
			}
		}
	}()
	backoff := time.Duration(0)
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			// a transient failure (out of descriptors, an aborted connection) must not end the service
			if backoff == 0 {
				backoff = 5 * time.Millisecond
			} else if backoff *= 2; backoff > time.Second {
				backoff = time.Second
			}
			s.logf("procbind: accept: %v", err)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		select {
		case <-s.bucket:
		default:
			s.logf("procbind: connection rate exceeded")
			_ = c.Close()
			continue
		}
		select {
		case s.sem <- struct{}{}:
			go func() {
				defer func() { <-s.sem }()
				s.ServeConn(c)
			}()
		default:
			s.logf("procbind: too many connections")
			_ = c.Close()
		}
	}
}

// noFDReader reads from a unix connection and refuses ancillary data: a file descriptor passed to
// the service is closed and the connection ends.
type noFDReader struct{ c *net.UnixConn }

var errPassedFD = errors.New("procbind: ancillary data on the request")

func (r noFDReader) Read(p []byte) (int, error) {
	oob := make([]byte, 256)
	n, oobn, _, _, err := r.c.ReadMsgUnix(p, oob)
	if oobn > 0 {
		if msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn]); perr == nil {
			for _, m := range msgs {
				if fds, ferr := syscall.ParseUnixRights(&m); ferr == nil {
					for _, fd := range fds {
						_ = syscall.Close(fd)
					}
				}
			}
		}
		return 0, errPassedFD
	}
	return n, err
}

// peer reads what the kernel says about the other end: its uid and pid at connect time and a pidfd
// for that process.
func peer(c *net.UnixConn) (cred unix.Ucred, pidfd int, err error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return cred, -1, err
	}
	pidfd = -1
	var e1, e2 error
	if err := rc.Control(func(fd uintptr) {
		var u *unix.Ucred
		u, e1 = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e1 == nil {
			cred = *u
		}
		pidfd, e2 = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	}); err != nil {
		return cred, -1, err
	}
	if e1 != nil || e2 != nil {
		if e2 == nil && pidfd >= 0 {
			_ = unix.Close(pidfd) // do not keep a pidfd for a peer that is refused
		}
		if e1 != nil {
			return cred, -1, e1
		}
		return cred, -1, fmt.Errorf("SO_PEERPIDFD: %w", e2)
	}
	return cred, pidfd, nil
}

func deadlineOf(d time.Duration) time.Time { return time.Now().Add(d) }

func (s *Server) init() {
	s.once.Do(func() {
		n, r := s.MaxConns, s.Rate
		if n <= 0 {
			n = 32
		}
		if r <= 0 {
			r = 100
		}
		s.sem = make(chan struct{}, n)
		s.bucket = make(chan struct{}, r)
		for i := 0; i < r; i++ {
			s.bucket <- struct{}{}
		}
	})
}

func (s *Server) requestTimeout() time.Duration {
	if s.RequestTimeout > 0 {
		return s.RequestTimeout
	}
	return time.Second
}

func (s *Server) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 5 * time.Second
}

// ServeConn handles one connection and closes it. A connection that cannot be served (a peer of
// another uid, one the service cannot see, a malformed or slow request, a passed descriptor) is
// closed without an answer, which the verifier reads as not answered, a deny.
func (s *Server) ServeConn(c *net.UnixConn) {
	s.init()
	defer c.Close()
	// a failure inside the registry or the walk ends this connection, never the host process
	defer func() {
		if r := recover(); r != nil {
			s.logf("procbind: panic while serving: %v", r)
		}
	}()
	_ = c.SetDeadline(time.Now().Add(s.timeout()))
	lookup := s.peerOf
	if lookup == nil {
		lookup = peer
	}
	cred, pfd, err := lookup(c)
	if err != nil {
		s.logf("procbind: peer credentials: %v", err)
		if pfd >= 0 {
			_ = unix.Close(pfd)
		}
		return
	}
	caller := HandleFromPidfd(pfd)
	defer caller.Close()
	if int(cred.Uid) != s.UID {
		s.logf("procbind: refused a peer of uid %d", cred.Uid)
		return
	}
	// the pid seen through SO_PEERCRED and the one the pidfd names must agree; 0 is a peer this
	// service's pid namespace cannot see
	if pid, err := caller.Pid(); cred.Pid <= 0 || err != nil || pid != int(cred.Pid) {
		s.logf("procbind: the peer is not visible in this pid namespace (cred pid %d)", cred.Pid)
		return
	}
	ns, err := NewNonce()
	if err != nil {
		return
	}
	nsHex := hex.EncodeToString(ns)
	if err := writeFrame(c, wireHello{V: protoVersion, Generation: s.Generation, NS: nsHex}); err != nil {
		return
	}
	if d := time.Now().Add(s.requestTimeout()); d.Before(deadlineOf(s.timeout())) {
		_ = c.SetReadDeadline(d) // a verifier that does not send its request promptly is dropped
	}
	var req wireRequest
	if err := readFrame(noFDReader{c}, &req); err != nil {
		s.logf("procbind: request: %v", err)
		return
	}
	nv, err := hex.DecodeString(req.NV)
	if req.V != protoVersion || err != nil || len(nv) != NonceLen ||
		subtle.ConstantTimeCompare([]byte(req.NS), []byte(nsHex)) != 1 ||
		req.Profile != s.Profile || req.TokenID == "" {
		s.logf("procbind: refused a malformed or foreign request")
		return
	}
	verdict, class := VerdictDeny, "denied"
	if worker, ok := s.Registry.Lookup(req.TokenID, req.Profile, req.Scope); !ok {
		s.logf("procbind: no active registration for the token")
	} else if r := Walk(s.Procs, caller, worker); !r.Allowed {
		s.logf("procbind: walk refused: %s", r.Reason)
	} else {
		verdict, class = VerdictAllow, ""
	}
	a := Answer{NV: nv, NS: ns, Generation: s.Generation, TokenID: req.TokenID, Profile: req.Profile, Scope: req.Scope, Verdict: verdict}
	mac, err := a.MAC(s.Key)
	if err != nil {
		s.logf("procbind: answer: %v", err)
		return
	}
	_ = writeFrame(c, wireReply{V: protoVersion, Verdict: string(verdict), Class: class, MAC: mac})
}
