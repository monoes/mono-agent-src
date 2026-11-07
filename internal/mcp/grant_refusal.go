package mcp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/orgbridge"
)

// grantRefusalLedgerTimeout bounds the refusal row's write. The write runs
// after the refusal is returned, so it adds nothing to the answer.
var grantRefusalLedgerTimeout = 250 * time.Millisecond

// grantRefusalNow is the clock of the row limit (replaced in tests).
var grantRefusalNow = time.Now

// grantRefusalFailBackoff is the quiet period after a failed attempt (scope
// check or write): no attempt of any name starts before it passes.
const grantRefusalFailBackoff = 5 * time.Second

const (
	// grantRefusalEvery is the least gap between two ledger rows for the same
	// never-served name: the first refusal per minute is recorded.
	grantRefusalEvery = time.Minute
	// grantRefusalMaxNames caps the names remembered; a flood of distinct
	// names beyond it writes nothing more until entries age out.
	grantRefusalMaxNamesDefault = 128
)

// grantRefusalMaxNames is the cap on remembered names (a var for tests).
var grantRefusalMaxNames = grantRefusalMaxNamesDefault

// Test hooks: called by the abandonable work before the runtime is opened
// and after the scope is resolved.
var grantRefusalBeforeScope, grantRefusalAfterScope func()

// refusalLog limits and tracks the ledger rows of never-served names. Its
// zero value is ready. One write is in flight at a time; a refusal that finds
// one running leaves no row.
type refusalLog struct {
	mu   sync.Mutex
	last map[string]time.Time
	busy bool
	// failUntil: no attempt starts before it (set by a failed attempt).
	failUntil time.Time
	wg        sync.WaitGroup // the slot holder: held at most grantRefusalLedgerTimeout
	work      sync.WaitGroup // the abandonable work itself (tests wait on it)
}

// refusalSlot is what an admitted attempt runs with, all captured under the
// log's lock so the abandoned work reads no shared variable afterwards.
type refusalSlot struct {
	epoch                   uint64
	beforeScope, afterScope func()
}

// admit reports whether a row for this name may be written now, and takes the
// single write slot when it may. A failed attempt backs every name off for
// grantRefusalFailBackoff, so a refusal that fails fast (wrong profile, bad
// environment, no grant row, database error) cannot make a database open per
// call.
func (l *refusalLog) admit(s *Server, name string) (refusalSlot, bool) {
	now := grantRefusalNow()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy || now.Before(l.failUntil) {
		return refusalSlot{}, false
	}
	if t, ok := l.last[name]; ok && now.Sub(t) < grantRefusalEvery {
		return refusalSlot{}, false
	}
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if len(l.last) >= grantRefusalMaxNames {
		for k, t := range l.last {
			if now.Sub(t) >= grantRefusalEvery {
				delete(l.last, k)
			}
		}
		if _, ok := l.last[name]; !ok && len(l.last) >= grantRefusalMaxNames {
			return refusalSlot{}, false
		}
	}
	l.busy = true
	l.wg.Add(1)
	return refusalSlot{epoch: s.currentEpoch(), beforeScope: grantRefusalBeforeScope, afterScope: grantRefusalAfterScope}, true
}

// fail starts the back-off after an attempt that wrote no row.
func (l *refusalLog) fail() {
	l.mu.Lock()
	l.failUntil = grantRefusalNow().Add(grantRefusalFailBackoff)
	l.mu.Unlock()
}

func (l *refusalLog) release() {
	l.mu.Lock()
	l.busy = false
	l.mu.Unlock()
	l.wg.Done()
}

// mark remembers a name whose row was written, so a failed or dropped write
// does not silence the next refusal of it.
func (l *refusalLog) mark(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if len(l.last) < grantRefusalMaxNames || l.last[name] != (time.Time{}) {
		l.last[name] = grantRefusalNow()
	}
}

// wait returns when no row write holds the slot (at most the timeout).
func (l *refusalLog) wait() { l.wg.Wait() }

// record leaves, in the background, the ledger row a refused tool call has
// always left (the row grantRun writes for a tool the grant lacks, same
// helper and fields). It runs after the refusal is decided, on a fresh
// context of grantRefusalLedgerTimeout, and its failure is ignored: the
// refusal never depends on the database. The row's profile, org, role and
// grant come from the stored grant, resolved here with the same scope checks
// as any call; when the process's --profile or environment does not match
// that grant, no row is written.
func (l *refusalLog) record(s *Server, name string) {
	slot, ok := l.admit(s, name)
	if !ok {
		return
	}
	epoch := slot.epoch
	l.work.Add(1)
	go func() {
		defer l.release()
		ctx, cancel := context.WithTimeout(context.Background(), grantRefusalLedgerTimeout)
		defer cancel()
		// The driver may not honour ctx while it waits on a lock, so the
		// work runs apart and is abandoned at the deadline. It builds no
		// runtime once closeRuntime has run, reads the database handle once
		// under the runtime's lock, and never panics the process. An
		// abandoned one stuck in the driver can outlive the timeout; the
		// back-off after a failed attempt starts at most one more per
		// grantRefusalFailBackoff, so they are bounded by time, not capped.
		done := make(chan struct{})
		var wrote atomic.Bool
		go func() {
			defer l.work.Done()
			defer close(done)
			defer func() { _ = recover() }()
			if slot.beforeScope != nil {
				slot.beforeScope()
			}
			b, rt, err := s.grantScopeAt(ctx, &epoch)
			if err != nil {
				return
			}
			if slot.afterScope != nil {
				slot.afterScope()
			}
			db := rt.sqlDB()
			if db == nil {
				return
			}
			if orgbridge.NewLedger(db).Refuse(ctx, orgbridge.Call{ProfileID: b.ProfileID, Direction: orgbridge.DirRoleTool, OrgName: b.OrgName, RoleID: b.RoleID, GrantID: b.ID}, orgbridge.StatusRefusedGrant) == nil {
				wrote.Store(true)
				l.mark(name)
			}
		}()
		select {
		case <-done:
			if !wrote.Load() {
				l.fail()
			}
		case <-ctx.Done():
			l.fail()
		}
	}()
}

func (s *Server) currentEpoch() uint64 {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()
	return s.epoch
}
