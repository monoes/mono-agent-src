package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// No run, no mode, and a run once known stays guarded.
//
// While the profile has no run that is not quiescent, every part of the engine is inert: it builds nothing, pins nothing,
// reads no key, logs nothing and writes nothing, and a doomsday_runs table that cannot be listed (missing, busy, locked,
// closed, corrupt) does not stop an ordinary dispatch. Once a run is KNOWN ACTIVE, an unreadable table refuses.
//
// "Unreadable" is store.ErrRunsUnreadable: the SELECT that lists the non-quiescent runs itself failed, so nothing says a run
// exists. A missing table (store.RunsTableMissing) is one kind of it. A run row that exists and does not verify, or a key
// that cannot be read, is never "unreadable": that is a run, and it refuses.
//
// A run is KNOWN ACTIVE when either holds:
//
//  1. this process has seen it: any listing, or the loop's precheck, returned a row for the profile, or found one that
//     failed to verify (RunActivity, shared by the gate, the governor and the strategy loop of the process, never
//     cleared for the process's life); or
//  2. the install has run traces: the run directory holds a per-run secret (stop.RunDir.HasRunTraces), which every run
//     creates and nothing deletes, so a host that was restarted, or a run entered by another process, is known even when
//     this process has not read the row.
//
// What is NOT known: a run entered by another process after this process's last successful read, on an install whose run
// directory has no secret yet, while the database is unreadable. The entering process writes the secret right after the
// row, and the strategy loop's precheck re-reads every few seconds, so the window is short; it is the cost of failing open.
type RunActivity struct{ known atomic.Bool }

// Known says whether this process has seen a run of the profile.
func (a *RunActivity) Known() bool { return a != nil && a.known.Load() }

func (a *RunActivity) markSeen() {
	if a != nil {
		a.known.Store(true)
	}
}

// failOpen folds one OpenRuns result into the record and says whether the dispatch may go on as if there were no run:
// only for an unreadable listing, with no run known by either rule.
func (a *RunActivity) failOpen(runs []store.Run, err error, dir stop.RunDir) bool {
	switch {
	case err == nil:
		if len(runs) > 0 {
			a.markSeen()
		}
		return false
	case errors.Is(err, store.ErrRunsUnreadable):
		return !a.Known() && !dir.HasRunTraces()
	default:
		a.markSeen() // a row that exists and does not verify, or a key that cannot be read: there is a run
		return false
	}
}

// OpenRunPrecheck is the strategy loop's precheck: one read-only COUNT of the profile's non-quiescent runs, no key and no
// pinned connection. True means a run is open (and records it); an unreadable table is false and quiet.
func OpenRunPrecheck(db *sql.DB, profile string, act *RunActivity) func(context.Context) bool {
	return func(ctx context.Context) bool {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent'`, profile).Scan(&n); err != nil || n == 0 {
			return false
		}
		act.markSeen()
		return true
	}
}
