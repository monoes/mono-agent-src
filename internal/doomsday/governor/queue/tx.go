package queue

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// tx runs fn in a BEGIN IMMEDIATE transaction on a connection of its own, so a read and the write that follows never
// interleave with another process. An error or a panic in fn rolls back. A BEGIN that reports an error is abandoned
// with a ROLLBACK that ignores ctx, so the connection never returns to the pool inside a transaction (#1315).
func (q *Queue) tx(ctx context.Context, fn func(x governor.DB) error) error {
	conn, err := q.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("governor queue: connection: %w", err)
	}
	defer conn.Close()
	if err := beginImmediate(ctx, conn); err != nil {
		abandonBegin(conn)
		return fmt.Errorf("governor queue: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if q.beforeCommit != nil {
		if err := q.beforeCommit(); err != nil {
			return err
		}
	}
	// The work is done: a context that ends now must not turn a commit that goes through into an error.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return fmt.Errorf("governor queue: commit: %w", err)
	}
	committed = true
	return nil
}

// abandonBegin puts a connection whose BEGIN reported an error back outside any transaction (see tasks.abandonBegin).
func abandonBegin(conn *sql.Conn) { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }

func isBusy(err error) bool {
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") || strings.Contains(m, "database table is locked")
}

// beginImmediate retries only a busy BEGIN: nothing has been written yet.
func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for attempt := 0; ; attempt++ {
		_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil || !isBusy(err) || attempt >= 3 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(100 * time.Millisecond << attempt):
		}
	}
}
