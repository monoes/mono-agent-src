package doomsday

import (
	"context"
	"database/sql"
	"sync/atomic"
)

// RunActivity remembers that this process has seen a run of the profile. Skeleton: filled in by the fix commit.
type RunActivity struct{ known atomic.Bool }

// OpenRunPrecheck is the loop's cheap precheck. Skeleton: always active.
func OpenRunPrecheck(db *sql.DB, profile string, act *RunActivity) func(context.Context) bool {
	return func(context.Context) bool { return true }
}

// Known says whether this process has seen a run. Skeleton.
func (a *RunActivity) Known() bool { return a.known.Load() }
