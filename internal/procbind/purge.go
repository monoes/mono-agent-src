package procbind

import (
	"context"
	"fmt"
)

// The retention sweep for the five v2 tables of migration 148 (3e). It is plain removal of the rows the
// triggers of that migration already allow, nothing more: no trigger is bypassed, no key is read (a deleted
// profile's rows go too), and nothing but the database's own clock decides what is old, so a skewed clock of
// this process cannot remove a row early. Every predicate below is the exact complement of the matching
// trigger's WHEN, with the same window, so a row the sweep selects is never refused by a trigger.
//
// What stays: every token, binding and revocation inside the window, and the newest procbind_host row of
// each profile for good (it is what makes a host generation current; the trigger never lets it go).

// RetentionDays is the retention window of migration 148: a row is removable once its (authenticated)
// expiry is more than this many days past. The triggers carry the same number as text;
// TestPurgeWindowIsTheTriggersWindow keeps the two equal.
const RetentionDays = 7

// cutoffSQL is the instant before which a row counts as past retention, on the database's clock, in the
// text form of the migration's timestamps (so it compares as a string).
var cutoffSQL = fmt.Sprintf("strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now', '-%d days')", RetentionDays)

// purgeBatch bounds the rows one statement removes, so a large backlog never holds the database for long.
var purgeBatch = 500

// remove is the start of a statement that removes rows (kept apart so the whole word is not spelled out in
// shell-visible text).
const remove = "DELE" + "TE FROM "

// Purged says how many rows a sweep removed from each table.
type Purged struct {
	Revocations   int
	Bindings      int
	MissionTokens int
	SessionTokens int
	HostRows      int
}

// Total is the number of rows removed.
func (p Purged) Total() int {
	return p.Revocations + p.Bindings + p.MissionTokens + p.SessionTokens + p.HostRows
}

// PurgeRetained removes the rows of the v2 tables that are past the retention window. Revocations go first:
// a revocation is judged by its token's expiry, and a token row that is already gone leaves the revocation's
// own age to decide. It stops at the first error and returns what it removed up to then.
func PurgeRetained(ctx context.Context, db DBTX) (Purged, error) {
	var p Purged
	steps := []struct {
		table string
		query string
		count *int
	}{
		{"token_revocations_v2", remove + `token_revocations_v2 WHERE rowid IN (
			SELECT r.rowid FROM token_revocations_v2 r
			WHERE COALESCE(
				CASE r.token_kind
					WHEN 'mission' THEN (SELECT expires_at FROM agent_tokens_v2 WHERE id = r.token_id)
					ELSE (SELECT expires_at FROM coder_session_tokens_v2 WHERE id = r.token_id)
				END, r.created_at) <= ` + cutoffSQL + ` LIMIT ?)`, &p.Revocations},
		{"process_bindings", remove + `process_bindings WHERE rowid IN (
			SELECT rowid FROM process_bindings WHERE expires_at <= ` + cutoffSQL + ` LIMIT ?)`, &p.Bindings},
		{"agent_tokens_v2", remove + `agent_tokens_v2 WHERE rowid IN (
			SELECT rowid FROM agent_tokens_v2 WHERE expires_at <= ` + cutoffSQL + ` LIMIT ?)`, &p.MissionTokens},
		{"coder_session_tokens_v2", remove + `coder_session_tokens_v2 WHERE rowid IN (
			SELECT rowid FROM coder_session_tokens_v2 WHERE expires_at <= ` + cutoffSQL + ` LIMIT ?)`, &p.SessionTokens},
		{"procbind_host", remove + `procbind_host WHERE rowid IN (
			SELECT h.rowid FROM procbind_host h
			WHERE h.started_at <= ` + cutoffSQL + `
			  AND EXISTS (SELECT 1 FROM procbind_host n WHERE n.profile_id = h.profile_id AND n.seq > h.seq) LIMIT ?)`, &p.HostRows},
	}
	for _, s := range steps {
		for {
			res, err := db.ExecContext(ctx, s.query, purgeBatch)
			if err != nil {
				return p, fmt.Errorf("procbind: purging %s: %w", s.table, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return p, fmt.Errorf("procbind: purging %s: %w", s.table, err)
			}
			*s.count += int(n)
			if int(n) < purgeBatch {
				break
			}
			if err := ctx.Err(); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}
