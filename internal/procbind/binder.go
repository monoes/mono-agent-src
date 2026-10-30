package procbind

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// The in-memory registry of the binding service (design section 4 and 5, and the wiring note): which
// tokens are bound to which worker, and whether each is still usable. It is memory-only and is never rebuilt
// from the database, so a restart leaves every credential denied until its worker is launched and bound
// again. Nothing here opens a socket or touches a database; it holds the workers' pidfds.

// Kind says which family a token belongs to.
type Kind string

const (
	KindMission Kind = "mission"
	KindSession Kind = "session"
)

var (
	ErrClosed     = errors.New("procbind: the binder is closed")
	ErrRevoked    = errors.New("procbind: the token was revoked")
	ErrDuplicate  = errors.New("procbind: the token is already registered")
	ErrNotPending = errors.New("procbind: the entry is not pending (revoked, already active, or closed)")
	ErrUnknown    = errors.New("procbind: no such entry")
)

// EntrySpec is what is registered for one token.
type EntrySpec struct {
	TokenID string
	Kind    Kind
	Profile string
	Scope   string
	// Owner is a mission token's owner (the run); Session and Turn name a session token's conversation and turn.
	Owner   string
	Session string
	Turn    string
	// Expires is when the token ends; a revoked entry is remembered until then so its id cannot be registered again.
	Expires time.Time
}

// TokenRef names one token.
type TokenRef struct {
	Kind    Kind
	TokenID string
	Profile string
}

// Selector picks entries to revoke. Profile is required, and at least one of TokenID, Owner, Session or All;
// every field that is set must match (Kind, when set, is a filter). An empty selector matches nothing.
type Selector struct {
	Profile string
	Kind    Kind
	TokenID string
	// Owner matches the owner itself and its runs ("owner/..."), not another owner that merely starts with it.
	Owner   string
	Session string
	Turn    string
	All     bool
}

func (s Selector) matches(e *entry) bool {
	if s.Profile == "" || e.spec.Profile != s.Profile {
		return false
	}
	if s.TokenID == "" && s.Owner == "" && s.Session == "" && !s.All {
		return false
	}
	if s.Kind != "" && e.spec.Kind != s.Kind {
		return false
	}
	if s.TokenID != "" && e.spec.TokenID != s.TokenID {
		return false
	}
	if s.Owner != "" && e.spec.Owner != s.Owner && !strings.HasPrefix(e.spec.Owner, s.Owner+"/") {
		return false
	}
	if s.Session != "" && e.spec.Session != s.Session {
		return false
	}
	if s.Turn != "" && e.spec.Turn != s.Turn {
		return false
	}
	return true
}

type state int

const (
	statePending state = iota
	stateActive
	stateRevoked
)

type entry struct {
	spec   EntrySpec
	h      Handle
	state  state
	leases int
	closed bool // the handle has been closed
}

// Binder is the registry. One lock guards every field, so a decision made under it (activate, revoke,
// lookup, a lease's validity) is linearised with every other.
type Binder struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*entry
	closed  bool
}

// NewBinder returns an empty binder; now is the clock (time.Now when nil).
func NewBinder(now func() time.Time) *Binder {
	if now == nil {
		now = time.Now
	}
	return &Binder{now: now, entries: map[string]*entry{}}
}

// releaseLocked closes the handle once the entry is revoked and no lease borrows it.
func (e *entry) releaseLocked() {
	if e.state == stateRevoked && e.leases == 0 && !e.closed {
		e.closed = true
		if e.h != nil {
			_ = e.h.Close()
		}
	}
}

// pruneLocked forgets revoked entries whose handle is closed and whose token has expired.
func (b *Binder) pruneLocked() {
	now := b.now()
	for id, e := range b.entries {
		if e.state == stateRevoked && e.closed && !now.Before(e.spec.Expires) {
			delete(b.entries, id)
		}
	}
}

// Register adds a pending entry and takes ownership of h (it closes it when the entry is revoked and no
// lease is out). On any error the caller still owns h.
func (b *Binder) Register(s EntrySpec, h Handle) error {
	if h == nil || s.TokenID == "" || s.Profile == "" || (s.Kind != KindMission && s.Kind != KindSession) || s.Expires.IsZero() {
		return errors.New("procbind: an entry needs a token id, a kind, a profile, an expiry and a handle")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	b.pruneLocked()
	if e, ok := b.entries[s.TokenID]; ok {
		if e.state == stateRevoked {
			return ErrRevoked
		}
		return ErrDuplicate
	}
	b.entries[s.TokenID] = &entry{spec: s, h: h, state: statePending}
	return nil
}

// Activate makes a pending entry usable. It is a compare-and-set: it fails if the entry was revoked or the
// binder closed since it was registered.
func (b *Binder) Activate(tokenID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	e, ok := b.entries[tokenID]
	if !ok {
		return ErrUnknown
	}
	if e.state != statePending {
		return ErrNotPending
	}
	e.state = stateActive
	return nil
}

// Revoke ends every entry the selector matches (pending or active) and returns the ones that were newly
// revoked. A revoked entry stays as a tombstone until its token's expiry.
func (b *Binder) Revoke(sel Selector) []TokenRef {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []TokenRef
	for _, e := range b.entries {
		if e.state != stateRevoked && sel.matches(e) {
			e.state = stateRevoked
			out = append(out, TokenRef{Kind: e.spec.Kind, TokenID: e.spec.TokenID, Profile: e.spec.Profile})
			e.releaseLocked()
		}
	}
	return out
}

// Reap ends the entry of a worker that has exited.
func (b *Binder) Reap(tokenID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.entries[tokenID]; ok && e.state != stateRevoked {
		e.state = stateRevoked
		e.releaseLocked()
	}
}

// Close revokes everything and refuses further use. A handle that a lease still borrows is closed by its
// last release.
func (b *Binder) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, e := range b.entries {
		e.state = stateRevoked
		e.releaseLocked()
	}
}

// Stats counts the entries by state (Revoked counts tombstones).
type Stats struct{ Pending, Active, Revoked int }

func (b *Binder) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()
	var st Stats
	for _, e := range b.entries {
		switch e.state {
		case statePending:
			st.Pending++
		case stateActive:
			st.Active++
		default:
			st.Revoked++
		}
	}
	return st
}

// Lease is a borrowed, counted hold on an active entry and its worker handle.
type Lease struct {
	b        *Binder
	e        *entry
	released bool // guarded by b.mu
}

// Acquire looks up an active entry for exactly this token, profile and scope and takes a lease on it. A
// pending, revoked, expired, unknown or mismatched entry gives nothing, and the caller cannot tell which.
func (b *Binder) Acquire(tokenID, profile, scope string) (*Lease, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	b.pruneLocked()
	e, ok := b.entries[tokenID]
	if !ok || e.state != stateActive || e.spec.Profile != profile || e.spec.Scope != scope || !b.now().Before(e.spec.Expires) {
		return nil, false
	}
	e.leases++
	return &Lease{b: b, e: e}, true
}

// Handle is the worker's handle, borrowed from the entry: do not close it. Nil once the lease is released.
func (l *Lease) Handle() Handle {
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	if l.released {
		return nil
	}
	return l.e.h
}

// Valid says, under the registry lock, whether the entry is still active and unexpired. The service calls it
// immediately before signing an allow: a revoke, a reap, a Close or an expiry that happened since the lookup
// makes it false. A revoke that completes after this call is not recalled (an allow is a point-in-time decision).
func (l *Lease) Valid() bool {
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	return !l.released && !l.b.closed && l.e.state == stateActive && l.b.now().Before(l.e.spec.Expires)
}

// Release gives the lease back; the last release of a revoked entry closes its handle. It is idempotent.
func (l *Lease) Release() {
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	l.e.leases--
	l.e.releaseLocked()
}
