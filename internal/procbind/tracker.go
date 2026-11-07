package procbind

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// peerKey is the identity a peer's limits are kept under: its pid and the time that process started
// (field 22 of /proc/<pid>/stat). A pid the kernel hands to a new process is a different key, so the new
// process starts with a full allowance instead of inheriting the drained one of its predecessor.
//
// The start time is in clock ticks (10 ms), so a pid recycled within one tick would share it. Where the
// kernel numbers process file descriptors (pidfs, Linux 6.9) the key also carries id, the inode number of the
// peer's pidfd, which names one process incarnation and is never reused; elsewhere id is 0 and the same-tick
// reuse is the residual SECURITY.md states.
type peerKey struct {
	pid   int
	start uint64
	id    uint64
}

// connState is one tracked connection; idle until the first byte of its request has arrived.
type connState struct {
	c       *net.UnixConn
	key     peerKey
	seq     uint64 // admission order, for choosing the oldest idle connection
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
	mu       sync.Mutex
	max      int
	perPeer  int
	perRate  int
	total    int
	seq      uint64
	peers    map[peerKey]*peerState
	lastPrun time.Time
	now      func() time.Time // time.Now unless a test supplies a clock
}

func (t *tracker) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// maxPeerRecords bounds the peer table. Peers with a connection are at most max (one connection each at
// least); the rest are idle records kept only until their allowance has refilled.
func maxPeerRecords(t *tracker) int { return t.max + 1024 }

// admit decides one new connection of the peer key. The peer's own rate and cap are checked first. If
// every slot is taken, a victim is chosen but NOT yet touched; only when the shared rate token
// (takeGlobal) has been taken is the victim closed and the newcomer admitted, so a refused newcomer
// never costs another connection its slot.
func (t *tracker) admit(key peerKey, c *net.UnixConn, takeGlobal func() bool) (*connState, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	t.prune(now)
	p := t.peers[key]
	isNew := p == nil
	if isNew {
		p = &peerState{tokens: float64(t.perRate), last: now}
	}
	tokens := p.tokens + now.Sub(p.last).Seconds()*float64(t.perRate)
	if tokens > float64(t.perRate) {
		tokens = float64(t.perRate)
	}
	if tokens < 1 {
		return nil, "peer connection rate exceeded"
	}
	if len(p.conns) >= t.perPeer {
		return nil, "peer connection limit reached"
	}
	var victim *connState
	var victimPeer *peerState
	var victimKey peerKey
	if t.total >= t.max {
		if victim, victimPeer, victimKey = t.pickVictim(len(p.conns)); victim == nil {
			return nil, "no free slot"
		}
	}
	if !takeGlobal() {
		return nil, "connection rate exceeded"
	}
	if victim != nil {
		t.remove(victimPeer, victim)
		_ = victimKey
		_ = victim.c.Close() // its handler sees a closed connection and ends
	}
	if isNew {
		if len(t.peers) >= maxPeerRecords(t) {
			t.dropOldestEmpty()
		}
		t.peers[key] = p
	}
	p.tokens, p.last = tokens-1, now
	t.seq++
	cs := &connState{c: c, key: key, seq: t.seq, idle: true}
	p.conns = append(p.conns, cs)
	t.total++
	return cs, ""
}

// pickVictim chooses the connection to take when every slot is busy: among the peers that hold more
// connections than the newcomer (mine) and have an idle one, the peer with the most; among equals the one
// whose oldest idle connection is the oldest. The choice does not depend on map order.
func (t *tracker) pickVictim(mine int) (*connState, *peerState, peerKey) {
	var best *connState
	var bestPeer *peerState
	var bestKey peerKey
	bestN := 0
	for key, p := range t.peers {
		if len(p.conns) <= mine {
			continue
		}
		var oldest *connState
		for _, cs := range p.conns {
			if cs.idle && (oldest == nil || cs.seq < oldest.seq) {
				oldest = cs
			}
		}
		if oldest == nil {
			continue
		}
		if best == nil || len(p.conns) > bestN || (len(p.conns) == bestN && oldest.seq < best.seq) {
			best, bestPeer, bestKey, bestN = oldest, p, key, len(p.conns)
		}
	}
	return best, bestPeer, bestKey
}

func (t *tracker) remove(p *peerState, cs *connState) {
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
}

// prune forgets peers with no connection whose allowance has had time to refill (a full burst takes
// perRate/perRate = 1 s), at most once a second; a recycled pid is a different key anyway (peerKey), and a key that comes back starts from a full allowance.
func (t *tracker) prune(now time.Time) {
	if now.Sub(t.lastPrun) < time.Second && len(t.peers) < maxPeerRecords(t) {
		return
	}
	t.lastPrun = now
	for key, p := range t.peers {
		if len(p.conns) == 0 && now.Sub(p.last) >= time.Second {
			delete(t.peers, key)
		}
	}
}

// dropOldestEmpty makes room in a full table by forgetting the idle record seen longest ago.
func (t *tracker) dropOldestEmpty() {
	var oldestKey peerKey
	found := false
	var oldest time.Time
	for key, p := range t.peers {
		if len(p.conns) == 0 && (!found || p.last.Before(oldest)) {
			oldestKey, oldest, found = key, p.last, true
		}
	}
	if found {
		delete(t.peers, oldestKey)
	}
}

func (t *tracker) release(cs *connState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := t.peers[cs.key]; p != nil {
		t.remove(p, cs)
	}
}

// busy marks a connection whose request has begun to arrive: it is working, not waiting, and is not
// evicted. It reports false if the connection was already taken, so the handler stops instead of
// answering on a closed connection.
func (t *tracker) busy(cs *connState) bool {
	if cs == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cs.removed {
		return false
	}
	cs.idle = false
	return true
}

var errEvicted = errors.New("procbind: the connection was taken for another peer")

// busyReader marks its connection busy, atomically with the first byte read, and fails if the
// connection had been taken before that.
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
