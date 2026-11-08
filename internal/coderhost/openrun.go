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
