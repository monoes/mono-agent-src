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
	// MaxPerPeer is how many connections one peer process may hold at once (4 by default), and
	// PerPeerRate the connections per second (and burst) one peer process may open (10 by default).
	// A same-uid process that floods the service is held to its own share instead of taking every
	// slot and the whole rate, which would make every other verification fail closed.
	MaxPerPeer  int
	PerPeerRate int

	once   sync.Once
	bucket chan struct{}
	track  tracker
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
		pid, err := peerPID(c)
		if err != nil {
			s.logf("procbind: peer credentials: %v", err)
			_ = c.Close()
			continue
		}
		// the peer's own share is checked first, so a flooder spends its own allowance and not the shared rate
		cs, why := s.track.admit(pid, c)
		if cs == nil {
			s.logf("procbind: refused a connection of pid %d: %s", pid, why)
			_ = c.Close()
			continue
		}
		select {
		case <-s.bucket:
		default:
			s.logf("procbind: connection rate exceeded")
			s.track.release(cs)
			_ = c.Close()
			continue
		}
		go func() {
			defer s.track.release(cs)
			s.serve(c, cs)
		}()
	}
}

// peerPID is the pid the kernel reports for the other end, read before anything else is done for it.
func peerPID(c *net.UnixConn) (int, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var cerr error
	if err := rc.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return int(cred.Pid), nil
}

// connState is one tracked connection; idle until its request has been read.
type connState struct {
	c       *net.UnixConn
	pid     int
	idle    bool
	removed bool
}

type peerState struct {
	conns  []*connState
	tokens float64
	last   time.Time
}

// tracker keeps the connections of each peer process: a cap per peer, a connection rate per peer, and,
// when every slot is taken, the right of a peer with fewer connections to take an idle slot from the
// peer with the most. A process that floods the service is held to its share (and its idle connections
// are the first to go) instead of denying everyone else.
type tracker struct {
	mu      sync.Mutex
	max     int
	perPeer int
	perRate int
	total   int
	peers   map[int]*peerState
}

func (t *tracker) admit(pid int, c *net.UnixConn) (*connState, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.peers[pid]
	if p == nil {
		p = &peerState{tokens: float64(t.perRate), last: time.Now()}
		t.peers[pid] = p
	}
	now := time.Now()
	p.tokens += now.Sub(p.last).Seconds() * float64(t.perRate)
	if p.tokens > float64(t.perRate) {
		p.tokens = float64(t.perRate)
	}
	p.last = now
	if p.tokens < 1 {
		t.dropIfEmpty(pid, p)
		return nil, "peer connection rate exceeded"
	}
	if len(p.conns) >= t.perPeer {
		return nil, "peer connection limit reached"
	}
	if t.total >= t.max && !t.evictFromHeaviest(len(p.conns)) {
		t.dropIfEmpty(pid, p)
		return nil, "no free slot"
	}
	p.tokens--
	cs := &connState{c: c, pid: pid, idle: true}
	p.conns = append(p.conns, cs)
	t.total++
	return cs, ""
}

// evictFromHeaviest closes one idle connection of the peer holding the most, provided that peer holds
// more than the newcomer does (mine); it reports whether a slot was freed.
func (t *tracker) evictFromHeaviest(mine int) bool {
	var heavy *peerState
	var heavyPID int
	for pid, p := range t.peers {
		if heavy == nil || len(p.conns) > len(heavy.conns) {
			heavy, heavyPID = p, pid
		}
	}
	if heavy == nil || len(heavy.conns) <= mine {
		return false
	}
	for _, cs := range heavy.conns {
		if cs.idle {
			t.remove(heavyPID, heavy, cs)
			_ = cs.c.Close() // its handler sees a closed connection and ends
			return true
		}
	}
	return false
}

func (t *tracker) remove(pid int, p *peerState, cs *connState) {
	if cs.removed {
		return
	}
	cs.removed = true
	for i, x := range p.conns {
		if x == cs {
			p.conns = append(p.conns[:i], p.conns[i+1:]...)
			break
		}
	}
	t.total--
	t.dropIfEmpty(pid, p)
}

// dropIfEmpty forgets a peer with no connection and a full allowance, so the table does not grow with
// every pid that ever connected.
func (t *tracker) dropIfEmpty(pid int, p *peerState) {
	if len(p.conns) == 0 && p.tokens >= float64(t.perRate) {
		delete(t.peers, pid)
	}
}

func (t *tracker) release(cs *connState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.peers[cs.pid]; p != nil {
		t.remove(cs.pid, p, cs)
	}
}

// busy marks a connection whose request has been read: it is working, not waiting, and is not evicted.
func (t *tracker) busy(cs *connState) {
	if cs == nil {
		return
	}
	t.mu.Lock()
	cs.idle = false
	t.mu.Unlock()
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
		s.bucket = make(chan struct{}, r)
		per, perRate := s.MaxPerPeer, s.PerPeerRate
		if per <= 0 {
			per = 4
		}
		if perRate <= 0 {
			perRate = 10
		}
		s.track = tracker{max: n, perPeer: per, perRate: perRate, peers: map[int]*peerState{}}
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
func (s *Server) ServeConn(c *net.UnixConn) { s.serve(c, nil) }

// serve is ServeConn with the connection's tracking state, when it has one (Serve gives it one).
func (s *Server) serve(c *net.UnixConn, cs *connState) {
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
	s.track.busy(cs)
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
