package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// revokeNamedGrant removes the given secret names from the profile's explicit
// (named-secret) agent grant, so a changed or replaced value must be granted
// again by the operator. A grant naming no secret (the blanket default) is
// left alone; a grant whose last name goes is deleted outright, since an
// empty list would mean every secret. A database without the table is fine.
func revokeNamedGrant(ctx context.Context, db *sql.DB, profileID string, drop ...string) error {
	var raw sql.NullString
	err := db.QueryRowContext(ctx, `SELECT secret_names FROM profile_agent_secret_access WHERE profile_id = ?`, profileID).Scan(&raw)
	if err == sql.ErrNoRows || (err != nil && strings.Contains(err.Error(), "no such table")) {
		return nil
	}
	if err != nil {
		return err
	}
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var names []string
	if json.Unmarshal([]byte(raw.String), &names) != nil || len(names) == 0 {
		return nil
	}
	var keep []string
	for _, n := range names {
		dropped := false
		for _, d := range drop {
			if n == d {
				dropped = true
			}
		}
		if !dropped {
			keep = append(keep, n)
		}
	}
	if len(keep) == len(names) {
		return nil
	}
	if len(keep) == 0 {
		return SetAgentAccess(ctx, db, profileID, false)
	}
	b, _ := json.Marshal(keep)
	_, err = db.ExecContext(ctx, `UPDATE profile_agent_secret_access SET secret_names = ? WHERE profile_id = ?`, string(b), profileID)
	return err
}

// entryName is the current name of entry id, "" when it does not exist.
func entryName(ctx context.Context, db *sql.DB, profileID, id string) string {
	var n string
	_ = db.QueryRowContext(ctx, `SELECT name FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID).Scan(&n)
	return n
}
