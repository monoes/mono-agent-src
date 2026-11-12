//go:build windows

package procbind

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Registry is the service's view of active registrations: the worker a token is bound to, as a
// held handle, only while the registration is active (never rebuilt from a database).
type Registry interface {
	Lookup(tokenID, profile, scope string) (worker Handle, ok bool)
}

// LeaseRegistry is the Binder's view: a counted lease on an active entry, which the service checks is still valid
// immediately before it signs an allow (a revoke, a reap, a close or an expiry during the walk makes it a deny).
// The *Binder is one. When a Server has both, Leases is used.
type LeaseRegistry interface {
	Acquire(tokenID, profile, scope string) (*Lease, bool)
}

// Server answers verification requests on a unix socket, with the same contract as the Linux service. It learns the
// caller from the kernel (the peer pid of the connection), pins that process with a handle at accept and reads its
// user from the process, never from the request, and walks the host's process table from that handle to the
// registered worker. A peer of another user, a process that cannot be pinned or read, and anything not provably
// resolved is closed without an answer, which the verifier reads as a deny.
type Server struct {
	Profile    string
	Generation string
	Key        []byte // the answer key (K_ans)
	Registry   Registry
	Leases     LeaseRegistry // when set, used instead of Registry
	Procs      Procs
	// SID is the only user served (the string form of the host's own user SID). An empty SID serves nobody.
	SID      string
	Timeout  time.Duration // per connection; 5 s by default
	MaxConns int           // concurrent connections; 32 by default
	// Logf receives the reason of every refusal. It is for the operator: reasons are never sent.
	Logf func(format string, args ...any)

	// RequestTimeout is how long the service waits for the request after its hello; 1 s by default.
	RequestTimeout time.Duration
	// Rate is the connections accepted per second (and the burst); 100 by default.
	Rate int
	// MaxPerPeer and PerPeerRate: see the Linux service; 4 and 10 by default.
	MaxPerPeer  int
	PerPeerRate int

	once   sync.Once
	bucket chan struct{}
	track  tracker
	// peerOf pins the process at the other end; tests replace it to stand in for a peer the service cannot see.
	peerOf func(*net.UnixConn) (peerInfo, error)
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// PeerPidfdSupported: see ready_windows.go.

// peerKey is the identity a peer's limits are kept under: its pid and the creation time of that process (a pid the kernel
// hands to a new process is a different key).
type peerKey struct {
	pid   int
	start uint64
	id    uint64
}

var errEvicted = errors.New("procbind: the connection was taken for another peer")

// busyReader marks its connection busy, atomically with the first byte read, and fails if the connection had been taken before that.
type busyReader struct {
	r      io.Reader
	mark   func() bool
	marked bool
}

func (b *busyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 && !b.marked {
		b.marked = true
		if !b.mark() {
			return 0, errEvicted
		}
	}
	return n, err
}

type unixAcceptor interface {
	AcceptUnix() (*net.UnixConn, error)
}

func (s *Server) peer(c *net.UnixConn) (peerInfo, error) {
	if s.peerOf != nil {
		return s.peerOf(c)
	}
	return peerProcess(c)
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
		accepted := time.Now()
		// the peer is pinned once, here, right after accept, and the pinned process is the one served
		pi, err := s.peer(c)
		if err != nil {
			s.logf("procbind: peer: %v", err)
			_ = c.Close()
			continue
		}
		if createdAfterAccept(pi.proc.Created(), accepted) { // the connector existed when it connected: this is a recycled number
			s.logf("procbind: refused a peer created after its connection was accepted")
			_ = pi.proc.Close()
			_ = c.Close()
			continue
		}
		pid, _ := pi.proc.Pid()
		cs, why := s.track.admit(peerKey{pid: pid, start: pi.proc.Created()}, c, s.takeRateToken)
		if cs == nil {
			s.logf("procbind: refused a connection of pid %d: %s", pid, why)
			_ = pi.proc.Close()
			_ = c.Close()
			continue
		}
		go func() {
			defer s.track.release(cs)
			s.serve(c, cs, &pi)
		}()
	}
}

func (s *Server) takeRateToken() bool {
	select {
	case <-s.bucket:
		return true
	default:
		return false
	}
}

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
		s.track = tracker{max: n, perPeer: per, perRate: perRate, peers: map[peerKey]*peerState{}}
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

func deadlineOf(d time.Duration) time.Time { return time.Now().Add(d) }

// ServeConn handles one connection and closes it. A connection that cannot be served is closed without an answer.
func (s *Server) ServeConn(c *net.UnixConn) { s.serve(c, nil, nil) }

// serve handles c for the peer pi (pinned at accept; pinned here for ServeConn). It takes ownership of the peer's handle.
func (s *Server) serve(c *net.UnixConn, cs *connState, pi *peerInfo) {
	s.init()
	defer c.Close()
	defer func() {
		if r := recover(); r != nil {
			s.logf("procbind: panic while serving: %v", r)
		}
	}()
	_ = c.SetDeadline(time.Now().Add(s.timeout()))
	if pi == nil {
		got, err := s.peer(c)
		if err != nil {
			s.logf("procbind: peer: %v", err)
			return
		}
		pi = &got
	}
	caller := pi.proc
	defer caller.Close()
	if s.SID == "" || subtle.ConstantTimeCompare([]byte(pi.sid), []byte(s.SID)) != 1 {
		s.logf("procbind: refused a peer of another user")
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
	var in io.Reader = c // Windows unix sockets carry no descriptors, so there is nothing to refuse
	if cs != nil {       // from the first byte on, the connection is working and cannot be taken
		in = &busyReader{r: in, mark: func() bool { return s.track.busy(cs) }}
	}
	if err := readFrame(in, &req); err != nil {
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
	var (
		worker Handle
		lease  *Lease
		ok     bool
	)
	if s.Leases != nil {
		if lease, ok = s.Leases.Acquire(req.TokenID, req.Profile, req.Scope); ok {
			defer lease.Release()
			worker = lease.Handle()
		}
	} else if s.Registry != nil {
		worker, ok = s.Registry.Lookup(req.TokenID, req.Profile, req.Scope)
	}
	if !ok || worker == nil {
		s.logf("procbind: no active registration for the token")
	} else if r := Walk(s.Procs, caller, worker); !r.Allowed {
		s.logf("procbind: walk refused: %s", r.Reason)
	} else if lease != nil && !lease.Valid() {
		s.logf("procbind: the registration ended while the walk ran")
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
