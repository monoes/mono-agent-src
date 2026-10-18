package secrets

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ReservedEntry is the vault entry no write reaches unless its context
// allows it (AllowReservedWrite): httpapi-token, the HTTP API's bearer.
// Whoever writes it chooses which bearer the API accepts, so only the
// server, which makes it, and the user's own commands do. Reading it is
// as for any entry.
const ReservedEntry = "httpapi-token"

// ErrReservedEntry is the refusal of a write that reaches ReservedEntry.
var ErrReservedEntry = errors.New(`the vault entry "httpapi-token" is the HTTP API's bearer, which only the user writes`)

type reservedWriteKey struct{}

// AllowReservedWrite marks ctx: a write under it may reach ReservedEntry.
func AllowReservedWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, reservedWriteKey{}, true)
}

func reservedName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), ReservedEntry)
}

// checkReservedWrite refuses a write under a ctx that doesn't allow it
// when it would name an entry ReservedEntry, or when entry id (if any) is
// that entry already.
func checkReservedWrite(ctx context.Context, db *sql.DB, profileID, id string, names ...string) error {
	if allowed, _ := ctx.Value(reservedWriteKey{}).(bool); allowed {
		return nil
	}
	for _, n := range names {
		if reservedName(n) {
			return ErrReservedEntry
		}
	}
	if id == "" {
		return nil
	}
	var name string
	switch err := db.QueryRowContext(ctx, `SELECT name FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID).Scan(&name); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case reservedName(name):
		return ErrReservedEntry
	}
	return nil
}
