// Package dbconn holds the one rule about pooled SQLite connections that more than one package needs: what to do
// with a dedicated connection whose BEGIN reported an error.
package dbconn

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
)

// AbandonBegin makes sure a connection whose BEGIN reported an error goes back to the pool outside any
// transaction. The driver reports the end of the context even for a BEGIN that took effect (a cancellation that
// lands as the statement finishes), and the connection would then be handed to the next caller still inside a
// transaction: "cannot start a transaction within a transaction" (#1315, #1373). Rolling back when none is open
// is a harmless error ("cannot rollback - no transaction is active"), which is ignored. Any other failure of the
// ROLLBACK leaves the connection in an unknown state, so it is dropped from the pool instead of returned. The
// ROLLBACK does not use the caller's context: a cancelled one would skip it.
//
// Call it on the error path of a BEGIN, before the connection is closed (conn.Close returns it to the pool).
// It never changes the BEGIN error, which the caller still returns.
func AbandonBegin(conn *sql.Conn) {
	_, err := conn.ExecContext(context.Background(), "ROLLBACK")
	if err == nil || strings.Contains(err.Error(), "no transaction is active") {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}
