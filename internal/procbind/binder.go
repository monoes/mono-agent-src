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
	ErrStale      = errors.New("procbind: the entry is of an older registration of this token id")
	ErrExpired    = errors.New("procbind: the token has already expired")
)

// Generation numbers a registration. A token id may be registered again after the old token expired, so a delayed
// notification about the old worker (its exit, a late activate or revoke) must not touch the new entry: the
// operations that act on one entry carry the generation Register returned and ignore any other.
type Generation uint64

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
	// Expires is when the token ends. The entry stops answering then; a revoked entry is remembered until then so
	// its id cannot be registered again.
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
	// Generation, when not zero, restricts the selector to one registration of the token id; it is not a
	// criterion by itself.
	Generation Generation
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
	if s.Generation != 0 && e.gen != s.Generation {
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
	gen    Generation
	h      Handle
	state  state
	leases int
	closed bool        // the handle has been handed out to be closed
	stop   func() bool // stops the entry's expiry timer
}

// revoke ends the entry (idempotent) and stops its timer.
func (e *entry) revoke() {
	e.state = stateRevoked
	if e.stop != nil {
		e.stop()
		e.stop = nil
	}
}

// Binder is the registry. One lock guards every field, so a decision made under it (activate, revoke,
// lookup, a lease's validity) is linearised with every other. A worker's handle is never closed while the
// lock is held (a Close that called back into the Binder would deadlock, and a slow one would block every
// lookup): the methods collect the handles to close and close them after they unlock.
type Binder struct {
	mu        sync.Mutex
	now       func() time.Time
	after     func(d time.Duration, f func()) (stop func() bool)
	entries   map[entryKey]*entry
	closed    bool
	lastPrune time.Time
	nextGen   Generation
}

// entryKey: a token id is unique within a profile, not across profiles.
type entryKey struct{ profile, id string }

// NewBinder returns an empty binder; now is the clock (time.Now when nil). Each entry has an expiry timer, so
// the handle of a token that expires is closed even when nothing else calls the binder.
func NewBinder(now func() time.Time) *Binder {
	return NewBinderWithTimer(now, nil)
}

// NewBinderWithTimer is NewBinder with the timer supplied (time.AfterFunc when nil); tests fire it by hand.
func NewBinderWithTimer(now func() time.Time, after func(d time.Duration, f func()) (stop func() bool)) *Binder {
	if now == nil {
		now = time.Now
	}
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return &Binder{now: now, after: after, entries: map[entryKey]*entry{}}
}

// expire is the entry's timer: it ends an entry whose token has expired, closes its handle unless a lease still
// borrows it, and forgets it. An entry that is no longer the registered one (it was replaced) is left alone.
func (b *Binder) expire(k entryKey, e *entry) {
	var h Handle
	b.mu.Lock()
	if b.entries[k] == e && !b.now().Before(e.spec.Expires) {
		e.revoke()
		h = e.release()
		if e.closed {
			delete(b.entries, k)
		}
	}
	b.mu.Unlock()
	if h != nil {
		_ = h.Close()
	}
}

// release marks the handle of a revoked entry with no lease out as to be closed and returns it (nil when
// there is nothing to close). Called with the lock held; the caller closes the handle after unlocking.
func (e *entry) release() Handle {
	if e.state == stateRevoked && e.leases == 0 && !e.closed {
		e.closed = true
		return e.h
	}
	return nil
}

func closeAll(hs []Handle) {
	for _, h := range hs {
		if h != nil {
			_ = h.Close()
		}
	}
}

// pruneLocked ends every entry whose token has expired (whatever its state: an expired entry would otherwise
// keep its descriptor and its id until someone revoked it) and forgets those whose handle is closed. It runs
// on Register and Stats, and on a lookup at most once a second; a lookup checks expiry itself.
func (b *Binder) pruneLocked(toClose *[]Handle) {
	now := b.now()
	b.lastPrune = now
	for k, e := range b.entries {
		if !now.Before(e.spec.Expires) {
			e.revoke()
			if h := e.release(); h != nil {
				*toClose = append(*toClose, h)
			}
			if e.closed {
				delete(b.entries, k)
			}
		}
	}
}

// Register adds a pending entry and takes ownership of h (it closes it when the entry is revoked and no
// lease is out). On any error the caller still owns h.
//
// It returns the registration's generation, which Activate, Reap and a generation-restricted selector must carry.
func (b *Binder) Register(s EntrySpec, h Handle) (Generation, error) {
	if h == nil || s.TokenID == "" || s.Profile == "" || s.Scope == "" || (s.Kind != KindMission && s.Kind != KindSession) || s.Expires.IsZero() {
		return 0, errors.New("procbind: an entry needs a token id, a kind, a profile, a scope, an expiry and a handle")
	}
	var toClose []Handle
	defer func() { closeAll(toClose) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}
	b.pruneLocked(&toClose)
	d := s.Expires.Sub(b.now())
	if d <= 0 {
		return 0, ErrExpired
	}
	k := entryKey{s.Profile, s.TokenID}
	if e, ok := b.entries[k]; ok {
		if e.state == stateRevoked {
			return 0, ErrRevoked
		}
		return 0, ErrDuplicate
	}
	b.nextGen++
	e := &entry{spec: s, gen: b.nextGen, h: h, state: statePending}
	e.stop = b.after(d, func() { b.expire(k, e) })
	b.entries[k] = e
	return e.gen, nil
}

// Activate makes a pending entry usable. It is a compare-and-set: it fails if the entry was revoked, expired
// or the binder closed since it was registered.
func (b *Binder) Activate(profile, tokenID string, gen Generation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	e, ok := b.entries[entryKey{profile, tokenID}]
	if !ok || gen == 0 {
		return ErrUnknown
	}
	if e.gen != gen {
		return ErrStale
	}
	if e.state != statePending || !b.now().Before(e.spec.Expires) {
		return ErrNotPending
	}
	e.state = stateActive
	return nil
}

// Revoke ends every entry the selector matches (pending or active) and returns the ones that were newly
// revoked. A revoked entry stays as a tombstone until its token's expiry.
func (b *Binder) Revoke(sel Selector) []TokenRef {
	var toClose []Handle
	defer func() { closeAll(toClose) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []TokenRef
	for _, e := range b.entries {
		if e.state != stateRevoked && sel.matches(e) {
			e.revoke()
			out = append(out, TokenRef{Kind: e.spec.Kind, TokenID: e.spec.TokenID, Profile: e.spec.Profile})
			if h := e.release(); h != nil {
				toClose = append(toClose, h)
			}
		}
	}
	return out
}

// Reap ends the entry of a worker that has exited.
func (b *Binder) Reap(profile, tokenID string, gen Generation) {
	var toClose []Handle
	defer func() { closeAll(toClose) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.entries[entryKey{profile, tokenID}]; ok && e.gen == gen && e.state != stateRevoked {
		e.revoke()
		if h := e.release(); h != nil {
			toClose = append(toClose, h)
		}
	}
}

// Close revokes everything and refuses further use. A handle that a lease still borrows is closed by its
// last release.
func (b *Binder) Close() {
	var toClose []Handle
	defer func() { closeAll(toClose) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, e := range b.entries {
		e.revoke()
		if h := e.release(); h != nil {
			toClose = append(toClose, h)
		}
	}
}

// Stats counts the entries by state (Revoked counts tombstones).
type Stats struct{ Pending, Active, Revoked int }

func (b *Binder) Stats() Stats {
	var toClose []Handle
	defer func() { closeAll(toClose) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(&toClose)
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

// Lease is a borrowed, counted hold on an active entry and its worker handle. A nil Lease is safe to use:
// it is never valid and releasing it does nothing.
type Lease struct {
	b        *Binder
	e        *entry
	released bool // guarded by b.mu
}

// Acquire looks up an active entry for exactly this token, profile and scope and takes a lease on it. A
// pending, revoked, expired, unknown or mismatched entry gives nothing, and the caller cannot tell which.
func (b *Binder) Acquire(tokenID, profile, scope string) (*Lease, bool) {
	var toClose []Handle
	defer func() { closeAll(toClose) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	if b.now().Sub(b.lastPrune) >= time.Second {
		b.pruneLocked(&toClose)
	}
	e, ok := b.entries[entryKey{profile, tokenID}]
	if !ok || e.state != stateActive || e.spec.Scope != scope || !b.now().Before(e.spec.Expires) {
		return nil, false
	}
	e.leases++
	return &Lease{b: b, e: e}, true
}

// Handle is the worker's handle, borrowed from the entry: do not close it. Nil once the lease is released.
func (l *Lease) Handle() Handle {
	if l == nil {
		return nil
	}
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
	if l == nil {
		return false
	}
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	if l.released || l.b.closed {
		return false
	}
	if !l.b.now().Before(l.e.spec.Expires) {
		l.e.revoke() // expiry ends the entry; the lease being asked keeps the handle open until it is released
		return false
	}
	return l.e.state == stateActive
}

// Release gives the lease back; the last release of a revoked entry closes its handle. It is idempotent.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	var h Handle
	l.b.mu.Lock()
	if !l.released {
		l.released = true
		l.e.leases--
		if !l.b.now().Before(l.e.spec.Expires) {
			l.e.revoke() // the token expired while the lease was out: the handle must not outlive it
		}
		h = l.e.release()
	}
	l.b.mu.Unlock()
	if h != nil {
		_ = h.Close()
	}
}
