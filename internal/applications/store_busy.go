package applications

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// Taking the write lock waits up to the connection's busy timeout (5 s). A writer that holds
// it longer, or that keeps winning it (the fill queue's tight loop under load), fails the
// BEGIN with SQLITE_BUSY; the BEGIN is retried a few times with a short backoff first. Only
// the BEGIN is retried: nothing has been read or written yet, so a retry repeats nothing.
const (
	busyRetries = 4
	busyBackoff = 100 * time.Millisecond
)

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") || strings.Contains(m, "database table is locked")
}

// beginImmediate opens the write transaction on conn, retrying only a busy database.
func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for attempt := 0; ; attempt++ {
		_, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if err == nil {
			return nil
		}
		// A busy BEGIN opens nothing, so the retries need no rollback between them; the connection is abandoned once,
		// when the loop gives up, so a rollback that drops it cannot turn a later attempt's error into sql.ErrConnDone.
		if !isBusy(err) || attempt >= busyRetries {
			dbconn.AbandonBegin(conn)
			return err
		}
		select {
		case <-ctx.Done():
			dbconn.AbandonBegin(conn)
			return err
		case <-time.After(busyBackoff << attempt):
		}
	}
}
