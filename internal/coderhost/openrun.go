package coderhost

import (
	"context"
	"database/sql"
)

// OpenRunCount is how many open-ended runs are open (not quiescent) in the database. `coder set` asks it inside the transaction
// that saves the setting, so a setting a run depends on is not changed under it without the CLI layer naming the run tables.
func OpenRunCount(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_runs WHERE state <> 'quiescent'`).Scan(&n)
	return n, err
}

// BoundRunsOpen counts the open-ended runs that are open and did NOT start unbound (no run.unbound audit row): their turns must be
// bound whatever the machine check says now.
func BoundRunsOpen(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_runs r WHERE r.state <> 'quiescent' AND NOT EXISTS
		(SELECT 1 FROM doomsday_audit a WHERE a.profile = r.profile AND a.generation = r.generation AND a.kind = 'run.unbound')`).Scan(&n)
	return n, err
}
