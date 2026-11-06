package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/orgbridge"
)

// grantRefusalLedgerTimeout bounds the refusal row's write. The write runs
// after the refusal is returned, so it adds nothing to the answer.
var grantRefusalLedgerTimeout = 250 * time.Millisecond

// grantRefusalNow is the clock of the row limit (replaced in tests).
var grantRefusalNow = time.Now

const (
	// grantRefusalEvery is the least gap between two ledger rows for the same
	// never-served name: the first refusal per minute is recorded.
	grantRefusalEvery = time.Minute
	// grantRefusalMaxNames caps the names remembered; a flood of distinct
	// names beyond it writes nothing more until entries age out.
	grantRefusalMaxNames = 128
)

// refusalLog limits and tracks the ledger rows of never-served names. Its
// zero value is ready. One write is in flight at a time; a refusal that finds
// one running leaves no row.
type refusalLog struct {
	mu   sync.Mutex
	last map[string]time.Time
	busy bool
	wg   sync.WaitGroup
}

// admit reports whether a row for this name may be written now, and takes the
// single write slot when it may.
func (l *refusalLog) admit(name string) bool {
	now := grantRefusalNow()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy {
		return false
	}
	if t, ok := l.last[name]; ok && now.Sub(t) < grantRefusalEvery {
		return false
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
			return false
		}
	}
	l.last[name] = now
	l.busy = true
	l.wg.Add(1)
	return true
}

func (l *refusalLog) release() {
	l.mu.Lock()
	l.busy = false
	l.mu.Unlock()
	l.wg.Done()
}

// wait returns when no row write is in flight.
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
	if !l.admit(name) {
		return
	}
	go func() {
		defer l.release()
		ctx, cancel := context.WithTimeout(context.Background(), grantRefusalLedgerTimeout)
		defer cancel()
		// The driver may not honour ctx while it waits on a lock, so the
		// work runs apart and is abandoned at the deadline: the slot and
		// wait() are held for at most grantRefusalLedgerTimeout.
		done := make(chan struct{})
		go func() {
			defer close(done)
			b, rt, err := s.grantScope(ctx)
			if err != nil {
				return
			}
			_ = orgbridge.NewLedger(rt.db.DB).Refuse(ctx, orgbridge.Call{ProfileID: b.ProfileID, Direction: orgbridge.DirRoleTool, OrgName: b.OrgName, RoleID: b.RoleID, GrantID: b.ID}, orgbridge.StatusRefusedGrant)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}()
}
